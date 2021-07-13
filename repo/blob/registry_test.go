package blob_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kopia/kopia/repo/blob"
)

type myConfig struct {
	Field int `json:"someField"`
}

type myStorage struct {
	blob.Storage

	cfg *myConfig
}

func TestRegistry(t *testing.T) {
	blob.AddSupportedStorage("mystorage", func() interface{} {
		return &myConfig{
			Field: 3,
		}
	}, func(c context.Context, i interface{}) (blob.Storage, error) {
		mc := i.(*myConfig)
		return &myStorage{cfg: mc}, nil
	})

	st, err := blob.NewStorage(context.Background(), blob.ConnectionInfo{
		Type: "mystorage",
		Config: &myConfig{
			Field: 4,
		},
	})

	require.NoError(t, err)
	require.IsType(t, (*myStorage)(nil), st)
	require.Equal(t, 4, st.(*myStorage).cfg.Field)

	_, err = blob.NewStorage(context.Background(), blob.ConnectionInfo{
		Type: "unknownstorage",
		Config: &myConfig{
			Field: 3,
		},
	})

	require.Error(t, err)
}

func TestConnectionInfo(t *testing.T) {
	blob.AddSupportedStorage("mystorage2", func() interface{} {
		return &myConfig{}
	}, func(c context.Context, i interface{}) (blob.Storage, error) {
		mc := i.(*myConfig)
		return &myStorage{cfg: mc}, nil
	})

	ci := blob.ConnectionInfo{
		Type: "mystorage2",
		Config: &myConfig{
			Field: 4,
		},
	}

	var ci2 blob.ConnectionInfo

	var buf bytes.Buffer

	require.NoError(t, json.NewEncoder(&buf).Encode(ci))
	require.NoError(t, json.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&ci2))
	require.Equal(t, ci, ci2)

	invalidJSON := []string{
		`[1,2,3]`,
		`{"type":"no-such-type","config":{}}`,
		`{"type":"mystorage2","config":3}`,
	}

	for _, tc := range invalidJSON {
		require.Error(t, json.NewDecoder(bytes.NewReader([]byte(tc))).Decode(&ci))
	}
}
