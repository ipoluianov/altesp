package ops

import (
	"context"
	"fmt"

	"github.com/ipoluianov/altesp/internal/formats/nvs"
)

// NVS is an NVS partition opened for editing
type NVS = nvs.Image

// NVSEntry is a key of NVS
type NVSEntry = nvs.Entry

// ReadNVS reads and parses an NVS partition of the board. An encrypted
// one is parsed with the keys of keysPartition when it is given ("" for
// none).
func (s *Session) ReadNVS(ctx context.Context, name, keysPartition string) (*NVS, error) {
	data, err := s.ReadPartition(ctx, name, nil)
	if err != nil {
		return nil, err
	}
	if keysPartition != "" {
		kd, err := s.ReadPartition(ctx, keysPartition, nil)
		if err != nil {
			return nil, err
		}
		k, err := nvs.ParseKeys(kd)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", keysPartition, err)
		}
		return nvs.ParseEncrypted(data, k)
	}
	return nvs.Parse(data)
}

// WriteNVS writes an edited NVS back into its partition. The image keeps
// its format version (v1 for firmware built before ESP-IDF v3.2) and its
// encryption.
func (s *Session) WriteNVS(ctx context.Context, name string, im *NVS) error {
	data, err := im.Bytes()
	if err != nil {
		return err
	}
	return s.WritePartition(ctx, name, data, true, nil)
}
