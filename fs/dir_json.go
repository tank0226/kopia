package fs

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kopia/kopia/cas"
)

const (
	maxDirectoryEntrySize = 65000
)

var (
	invalidDirectoryDataError = errors.New("invalid directory data")
)

type directoryEntry struct {
	name     string
	fileMode uint32
	fileSize int64
	modTime  time.Time
	userID   uint32
	groupID  uint32
	objectID string
}

type jsonDirectoryEntry struct {
	FileName    string    `json:"f,omitempty"`
	DirName     string    `json:"d,omitempty"`
	Permissions string    `json:"p,omitempty"`
	Size        string    `json:"s,omitempty"`
	Time        time.Time `json:"t"`
	Owner       string    `json:"o,omitempty"`
	ObjectID    string    `json:"oid,omitempty"`
}

func (de *directoryEntry) Name() string {
	return de.name
}

func (de *directoryEntry) Mode() os.FileMode {
	return os.FileMode(de.fileMode)
}

func (de *directoryEntry) IsDir() bool {
	return de.Mode().IsDir()
}

func (de *directoryEntry) Size() int64 {
	if de.Mode().IsRegular() {
		return de.fileSize
	}

	return 0
}

func (de *directoryEntry) UserID() uint32 {
	return de.userID
}

func (de *directoryEntry) GroupID() uint32 {
	return de.groupID
}

func (de *directoryEntry) ModTime() time.Time {
	return de.modTime
}

func (de *directoryEntry) ObjectID() cas.ObjectID {
	return cas.ObjectID(de.objectID)
}

func (de *directoryEntry) Sys() interface{} {
	return nil
}

func (de *directoryEntry) fromJSON(jde *jsonDirectoryEntry) error {
	var mode uint32

	switch {
	case jde.DirName != "":
		de.name = jde.DirName
		mode = uint32(os.ModeDir)

	case jde.FileName != "":
		de.name = jde.FileName
		mode = 0
	}

	if jde.Permissions != "" {
		s, err := strconv.ParseUint(jde.Permissions, 8, 32)
		if err != nil {
			return err
		}
		mode |= uint32(s)
	}

	de.fileMode = mode
	de.modTime = jde.Time
	if jde.Owner != "" {
		fmt.Sscanf(jde.Owner, "%d:%d", &de.userID, &de.groupID)
	}
	de.objectID = jde.ObjectID

	if jde.Size != "" {
		s, err := strconv.ParseInt(jde.Size, 10, 64)
		if err != nil {
			return err
		}
		de.fileSize = s
	}
	return nil
}

type directoryWriter struct {
	io.Closer

	writer    io.Writer
	buf       []byte
	separator []byte
}

func (dw *directoryWriter) WriteEntry(e Entry) error {
	var jde jsonDirectoryEntry

	m := e.Mode()
	switch m & os.ModeType {
	case os.ModeDir:
		jde.DirName = e.Name()
	default:
		jde.FileName = e.Name()
		jde.Size = strconv.FormatInt(e.Size(), 10)
	}

	jde.Permissions = strconv.FormatInt(int64(e.Mode()&os.ModePerm), 8)
	jde.Time = e.ModTime()
	jde.Owner = fmt.Sprintf("%d:%d", e.UserID(), e.GroupID())
	jde.ObjectID = string(e.ObjectID())

	v, _ := json.Marshal(&jde)

	dw.writer.Write(dw.separator)
	dw.writer.Write(v)
	dw.separator = []byte(",\n  ")

	return nil
}

func (dw *directoryWriter) Close() error {
	dw.writer.Write([]byte("\n]}\n"))
	return nil
}

func (*directoryWriter) serializeLengthPrefixedString(buf []byte, s string) int {
	offset := binary.PutUvarint(buf, uint64(len(s)))
	copy(buf[offset:], s)
	offset += len(s)
	return offset
}

func newDirectoryWriter(w io.Writer) *directoryWriter {
	dw := &directoryWriter{
		writer: w,
	}

	var f directoryFormat
	f.Version = 1

	io.WriteString(w, "{\n\"format\":")
	b, _ := json.Marshal(&f)
	w.Write(b)
	io.WriteString(w, ",\n\"entries\":[")
	dw.separator = []byte("\n  ")

	return dw
}

type directoryReader struct {
	reader  io.Reader
	decoder *json.Decoder
}

func (dr *directoryReader) ReadNext() (Entry, error) {
	if dr.decoder.More() {
		var jde jsonDirectoryEntry
		if err := dr.decoder.Decode(&jde); err != nil {
			return nil, err
		}

		var de directoryEntry
		if err := de.fromJSON(&jde); err != nil {
			return nil, err
		}

		return &de, nil
	}

	// Expect ']'
	t, err := dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if t != json.Delim(']') {
		return nil, fmt.Errorf("invalid directory data: expected ']', got %v", t)
	}

	// Expect '}'
	t, err = dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if t != json.Delim('}') {
		return nil, fmt.Errorf("invalid directory data: expected ']', got %v", t)
	}

	// Expect end of file
	t, err = dr.decoder.Token()
	if err != io.EOF {
		return nil, fmt.Errorf("invalid directory data: expected EOF, got %v", t)
	}

	return nil, io.EOF
}

type directoryFormat struct {
	Version int `json:"version"`
}

func newDirectoryReader(r io.Reader) (*directoryReader, error) {
	dr := &directoryReader{
		decoder: json.NewDecoder(r),
	}

	var t json.Token
	var err error

	// Expect opening '{'
	t, err = dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if t != json.Delim('{') {
		return nil, fmt.Errorf("invalid directory data: expected '{', got %v", t)
	}

	// Expect "format"
	t, err = dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if s, ok := t.(string); ok {
		if s != "format" {
			return nil, fmt.Errorf("invalid directory data: expected 'format', got '%v'", s)
		}
	} else {
		return nil, fmt.Errorf("invalid directory data: expected 'format', got '%v'", t)
	}

	// Parse format and trailing comma
	var format directoryFormat
	err = dr.decoder.Decode(&format)
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	// Expect "entries"
	t, err = dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if s, ok := t.(string); ok {
		if s != "entries" {
			return nil, fmt.Errorf("invalid directory data: expected 'entries', got '%v'", s)
		}
	} else {
		return nil, fmt.Errorf("invalid directory data: expected 'entries', got '%v'", t)
	}

	// Expect opening '['
	t, err = dr.decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid directory data: %v", err)
	}

	if t != json.Delim('[') {
		return nil, fmt.Errorf("invalid directory data: expected '[', got %v", t)
	}

	return dr, nil
}

func ReadDirectory(r io.Reader, namePrefix string) (Directory, error) {
	dr, err := newDirectoryReader(r)
	if err != nil {
		return nil, err
	}

	var dir Directory
	for {
		e, err := dr.ReadNext()
		if e != nil {
			dir = append(dir, e)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}

	return dir, nil
}