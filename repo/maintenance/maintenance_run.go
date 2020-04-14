// Package maintenance manages automatic repository maintenance.
package maintenance

import (
	"context"
	"time"

	"github.com/gofrs/flock"
	"github.com/pkg/errors"

	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/repo/logging"
	"github.com/kopia/kopia/repo/manifest"
)

var log = logging.GetContextLoggerFunc("maintenance")

// Mode describes the mode of maintenance to perfor
type Mode string

// MaintainableRepository is a subset of Repository required for maintenance tasks.
type MaintainableRepository interface {
	Username() string
	Hostname() string
	Time() time.Time
	ConfigFilename() string

	BlobStorage() blob.Storage
	ContentManager() *content.Manager

	GetManifest(ctx context.Context, id manifest.ID, data interface{}) (*manifest.EntryMetadata, error)
	PutManifest(ctx context.Context, labels map[string]string, payload interface{}) (manifest.ID, error)
	FindManifests(ctx context.Context, labels map[string]string) ([]*manifest.EntryMetadata, error)
	DeleteManifest(ctx context.Context, id manifest.ID) error
}

// Supported maintenance modes
const (
	ModeNone  Mode = "none"
	ModeQuick Mode = "quick"
	ModeFull  Mode = "full"
	ModeAuto  Mode = "auto" // run either quick of full if required by schedule
)

// shouldRun returns Mode if repository is due for periodic maintenance.
func shouldRun(ctx context.Context, rep MaintainableRepository, p *Params) (Mode, error) {
	if myUsername := rep.Username() + "@" + rep.Hostname(); p.Owner != myUsername {
		log(ctx).Debugf("maintenance owned by another user '%v'", p.Owner)
		return ModeNone, nil
	}

	s, err := GetSchedule(ctx, rep)
	if err != nil {
		return ModeNone, errors.Wrap(err, "error getting status")
	}

	// check full cycle first, as it does more than the quick cycle
	if p.FullCycle.Enabled {
		if rep.Time().After(s.NextFullMaintenanceTime) {
			log(ctx).Debugf("due for full manintenance cycle")
			return ModeFull, nil
		}

		log(ctx).Debugf("not due for full manintenance cycle until %v", s.NextFullMaintenanceTime)
	} else {
		log(ctx).Debugf("full manintenance cycle not enabled")
	}

	// no time for full cycle, check quick cycle
	if p.QuickCycle.Enabled {
		if rep.Time().After(s.NextQuickMaintenanceTime) {
			log(ctx).Debugf("due for quick manintenance cycle")
			return ModeQuick, nil
		}

		log(ctx).Debugf("not due for quick manintenance cycle until %v", s.NextQuickMaintenanceTime)
	} else {
		log(ctx).Debugf("quick manintenance cycle not enabled")
	}

	return ModeNone, nil
}

func updateSchedule(ctx context.Context, runParams RunParameters) error {
	rep := runParams.rep
	p := runParams.Params

	s, err := GetSchedule(ctx, rep)
	if err != nil {
		return errors.Wrap(err, "error getting schedule")
	}

	switch runParams.Mode {
	case ModeFull:
		// on full cycle, also update the quick cycle
		s.NextFullMaintenanceTime = rep.Time().Add(p.FullCycle.Interval)
		s.NextQuickMaintenanceTime = s.NextFullMaintenanceTime.Add(p.QuickCycle.Interval)
		log(ctx).Debugf("scheduling next full cycle at %v", s.NextFullMaintenanceTime)
		log(ctx).Debugf("scheduling next quick cycle at %v", s.NextQuickMaintenanceTime)

		return SetSchedule(ctx, rep, s)

	case ModeQuick:
		log(ctx).Debugf("scheduling next quick cycle at %v", s.NextQuickMaintenanceTime)
		s.NextQuickMaintenanceTime = rep.Time().Add(p.QuickCycle.Interval)

		return SetSchedule(ctx, rep, s)

	default:
		return nil
	}
}

// RunParameters passes essential parameters for maintenance.
// It is generated by RunExclusive and can't be create outside of its package and
// is required to ensure all maintenance tasks run under an exclusive lock.
type RunParameters struct {
	rep MaintainableRepository

	Mode Mode

	Params *Params
}

// RunExclusive runs the provided callback if the maintenance is owned by local user and
// lock can be acquired. Lock is passed to the function, which ensures that every call to Run()
// is within the exclusive context.
func RunExclusive(ctx context.Context, rep MaintainableRepository, mode Mode, cb func(runParams RunParameters) error) error {
	p, err := GetParams(ctx, rep)
	if err != nil {
		return errors.Wrap(err, "unable to get maintenance params")
	}

	if myUsername := rep.Username() + "@" + rep.Hostname(); p.Owner != myUsername {
		log(ctx).Debugf("maintenance owned by another user '%v'", p.Owner)
		return nil
	}

	if mode == ModeAuto {
		mode, err = shouldRun(ctx, rep, p)
		if err != nil {
			return errors.Wrap(err, "unable to determine if maintenance is required")
		}
	}

	if mode == ModeNone {
		log(ctx).Debugf("not due for maintenance")
		return nil
	}

	runParams := RunParameters{rep, mode, p}

	// update schedule so that we don't run the maintenance again immediately if
	// this process crashes.
	if err = updateSchedule(ctx, runParams); err != nil {
		return errors.Wrap(err, "error updating maintenance schedule")
	}

	lockFile := rep.ConfigFilename() + ".mlock"
	log(ctx).Debugf("Acquiring maintenance lock in file %v", lockFile)

	// acquire local lock on a config file
	l := flock.New(lockFile)

	ok, err := l.TryLock()
	if err != nil {
		return errors.Wrap(err, "error acquiring maintenance lock")
	}

	if !ok {
		log(ctx).Debugf("maintenance is already in progress locally")
		return nil
	}

	defer l.Unlock() //nolint:errcheck

	log(ctx).Infof("Running %v maintenance...", runParams.Mode)
	defer log(ctx).Infof("Finished %v maintenance.", runParams.Mode)

	return cb(runParams)
}

// Run performs maintenance activities for a repository.
func Run(ctx context.Context, runParams RunParameters) error {
	switch runParams.Mode {
	case ModeQuick:
		return runQuickMaintenance(ctx, runParams)

	case ModeFull:
		return runFullMaintenance(ctx, runParams)

	default:
		return errors.Errorf("unknown mode %q", runParams.Mode)
	}
}

func runQuickMaintenance(ctx context.Context, runParams RunParameters) error {
	// rewrite indexes by dropping content entries that have been marked
	// as deleted for a long time
	if err := DropDeletedContents(ctx, runParams.rep, &runParams.Params.DropDeletedContent); err != nil {
		return errors.Wrap(err, "error dropping deleted contents")
	}

	// find 'q' packs that are less than 80% full and rewrite contents in them into
	// new consolidated packs, orphaning old packs in the process.
	if err := RewriteContents(ctx, runParams.rep, &RewriteContentsOptions{
		ContentIDRange: content.AllPrefixedIDs,
		PackPrefix:     content.PackBlobIDPrefixSpecial,
		ShortPacks:     true,
	}); err != nil {
		return errors.Wrap(err, "error rewriting metadata contents")
	}

	// delete orphaned 'q' packs after some time.
	if _, err := DeleteUnreferencedBlobs(ctx, runParams.rep, DeleteUnreferencedBlobsOptions{
		Prefix: content.PackBlobIDPrefixSpecial,
	}); err != nil {
		return errors.Wrap(err, "error deleting unreferenced metadata blobs")
	}

	return nil
}

func runFullMaintenance(ctx context.Context, runParams RunParameters) error {
	// rewrite indexes by dropping content entries that have been marked
	// as deleted for a long time
	if err := DropDeletedContents(ctx, runParams.rep, &runParams.Params.DropDeletedContent); err != nil {
		return errors.Wrap(err, "error dropping deleted contents")
	}

	// find packs that are less than 80% full and rewrite contents in them into
	// new consolidated packs, orphaning old packs in the process.
	if err := RewriteContents(ctx, runParams.rep, &RewriteContentsOptions{
		ContentIDRange: content.AllIDs,
		ShortPacks:     true,
	}); err != nil {
		return errors.Wrap(err, "error rewriting contents in short packs")
	}

	// delete orphaned packs after some time.
	if _, err := DeleteUnreferencedBlobs(ctx, runParams.rep, DeleteUnreferencedBlobsOptions{}); err != nil {
		return errors.Wrap(err, "error deleting unreferenced blobs")
	}

	return nil
}