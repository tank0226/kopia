package maintenance_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kopia/kopia/internal/repotesting"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/maintenance"
	"github.com/kopia/kopia/repo/object"
)

func TestContentRewrite(t *testing.T) {
	cases := []struct {
		numPContents int
		numQContents int
		opt          *maintenance.RewriteContentsOptions
		wantPDelta   int
		wantQDelta   int
	}{
		{
			numPContents: 2,
			numQContents: 3,
			opt: &maintenance.RewriteContentsOptions{
				ShortPacks: true,
			},
			wantPDelta: 1,
			wantQDelta: 1,
		},
		{
			numPContents: 2,
			numQContents: 3,
			opt: &maintenance.RewriteContentsOptions{
				ShortPacks: true,
				DryRun:     true,
			},
			wantPDelta: 0,
			wantQDelta: 0,
		},
		{
			numPContents: 2,
			numQContents: 3,
			opt: &maintenance.RewriteContentsOptions{
				ShortPacks: true,
				PackPrefix: "p",
			},
			wantPDelta: 1,
			wantQDelta: 0,
		},
		{
			numPContents: 1,
			numQContents: 0,
			opt: &maintenance.RewriteContentsOptions{
				ShortPacks: true,
			},
			wantPDelta: 0, // single pack won't get rewritten
			wantQDelta: 0,
		},
		{
			numPContents: 1,
			numQContents: 1,
			opt: &maintenance.RewriteContentsOptions{
				ShortPacks: true,
			},
			wantPDelta: 0,
			wantQDelta: 0,
		},
	}

	for _, tc := range cases {
		tc := tc

		t.Run(fmt.Sprintf("case-%v", tc), func(t *testing.T) {
			ctx, env := repotesting.NewEnvironment(t)

			// run N sessions to create N individual pack blobs for each content prefix
			for i := 0; i < tc.numPContents; i++ {
				require.NoError(t, repo.WriteSession(ctx, env.Repository, repo.WriteSessionOptions{}, func(ctx context.Context, w repo.RepositoryWriter) error {
					ow := w.NewObjectWriter(ctx, object.WriterOptions{})
					fmt.Fprintf(ow, "%v", uuid.NewString())
					_, err := ow.Result()
					return err
				}))
			}

			for i := 0; i < tc.numQContents; i++ {
				require.NoError(t, repo.WriteSession(ctx, env.Repository, repo.WriteSessionOptions{}, func(ctx context.Context, w repo.RepositoryWriter) error {
					ow := w.NewObjectWriter(ctx, object.WriterOptions{Prefix: "k"})
					fmt.Fprintf(ow, "%v", uuid.NewString())
					_, err := ow.Result()
					return err
				}))
			}

			pBlobsBefore, err := blob.ListAllBlobs(ctx, env.RepositoryWriter.BlobStorage(), "p")
			require.NoError(t, err)

			qBlobsBefore, err := blob.ListAllBlobs(ctx, env.RepositoryWriter.BlobStorage(), "q")
			require.NoError(t, err)

			require.NoError(t, repo.DirectWriteSession(ctx, env.RepositoryWriter, repo.WriteSessionOptions{}, func(ctx context.Context, w repo.DirectRepositoryWriter) error {
				return maintenance.RewriteContents(ctx, w, tc.opt, maintenance.SafetyNone)
			}))

			pBlobsAfter, err := blob.ListAllBlobs(ctx, env.RepositoryWriter.BlobStorage(), "p")
			require.NoError(t, err)

			qBlobsAfter, err := blob.ListAllBlobs(ctx, env.RepositoryWriter.BlobStorage(), "q")
			require.NoError(t, err)

			require.Equal(t, tc.wantPDelta, len(pBlobsAfter)-len(pBlobsBefore), "invalid p blob count delta")
			require.Equal(t, tc.wantQDelta, len(qBlobsAfter)-len(qBlobsBefore), "invalid q blob count delta")
		})
	}
}
