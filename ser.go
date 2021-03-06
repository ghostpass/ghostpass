// Implements helper routines useful for de/serialization for both the stationary and plainsight states in order to
// securely JSONify secret store state for storage.
package ghostpass

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io/ioutil"
)

// Helper function that converts a stationary persistent store back into a `SecretStore` for interaction.
// Putting the store in stationary mode preserves more state than plainsight mode, so not much decryption is needed.
func StationaryUnmarshal(checksum [32]byte, serialized []byte) (*SecretStore, error) {

	// turn the serialized JSON back into a partially initialized state for a SecretStore
	var ss struct {
		Version    int               `json:"version"`
		StoreState string            `json:"state"`
		Name       string            `json:"name"`
		Fields     map[string]*Field `json:"fields"`
	}
	err := json.Unmarshal(serialized, &ss)
	if err != nil {
		return nil, err
	}

	// no need to decrypt service, since this it's not encrypted. We are also
	// not making a copy since we are just mutating the state of the fields
	for _, field := range ss.Fields {
		// rederive attributes of partial field
		if err := field.RederiveAuthPair(checksum[:]); err != nil {
			return nil, err
		}
	}

	// return the SecretStore as if nothing changed
	return &SecretStore{
		Version:      ss.Version,
		StoreState:   ss.StoreState,
		Name:         ss.Name,
		SymmetricKey: checksum[:],
		Fields:       ss.Fields,
	}, nil
}

// Helper routine that prepares a secret store from an exported plainsight
// distribution. Since the state stored on disk does not contain any remnants of the auth
// credentials per field, this unmarshaller rederives that using the given symmetric key.
func PlainsightUnmarshal(checksum [32]byte, encoded []byte) (*SecretStore, error) {

	// decode from base64
	compressed, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		return nil, err
	}

	// decompress the compressed input before deserializing
	reader, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}

	// parse out serialized JSON plainsight store
	serialized, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}

	// turn the serialized JSON back into a partially initialized state for a SecretStore
	var ss struct {
		Version    int               `json:"version"`
		StoreState string            `json:"state"`
		Name       string            `json:"name"`
		Fields     map[string][]byte `json:"fields"`
	}
	if err := json.Unmarshal(serialized, &ss); err != nil {
		return nil, err
	}

	// create new semi-unencrypted mapping
	fields := make(map[string]*Field)

	for servicekey, secret := range ss.Fields {

		// decode hex for key
		dec, err := hex.DecodeString(servicekey)
		if err != nil {
			return nil, err
		}

		// decrypt service key if store file was plainsight exported
		service, err := BoxDecrypt(checksum[:], []byte(dec))
		if err != nil {
			return nil, err
		}

		// reinitialize field from compressed secret
		field, err := ReconstructField(checksum[:], secret)
		if err != nil {
			return nil, err
		}

		// decomprethe string representation for secrets back into a field
		fields[string(service)] = field
		delete(ss.Fields, servicekey)
	}

	// return the SecretStore as if nothing changed
	return &SecretStore{
		Version:      ss.Version,
		StoreState:   StoreStationary,
		Name:         ss.Name,
		SymmetricKey: checksum[:],
		Fields:       fields,
	}, nil
}

// Helper routine that helps prepare a secret store to be plainsight distributable, by
// incorporating indistinguishability to all entries, stripping the symmetric key checksum,
// compressing the final store, and applying one-time pads for deniability (TODO).
func (ss *SecretStore) PlainsightMarshal() ([]byte, error) {

	// stores a final compressed mapping for the secret store's fields, where
	// keys are encrypted for indistinguishability and a compressed form of the credential pair
	// is also created to map against for serialization.
	encfields := make(map[string][]byte)

	// encrypt all the service keys for indistinguishability
	for service, field := range ss.Fields {

		// encrypt the service key for indistinguishability
		encservice, err := BoxEncrypt(ss.SymmetricKey, []byte(service))
		if err != nil {
			return nil, err
		}

		// TODO: if deniable secrets are found, apply one-time pad to mutate secret
		secret := field.AuthPair

		// store the new encrypted entry
		enc := hex.EncodeToString(encservice)
		encfields[enc] = secret
	}

	// serialize into a byte array for compression
	data, err := json.Marshal(&struct {
		Version    int               `json:"version"`
		StoreState string            `json:"state"`
		Name       string            `json:"name"`
		Fields     map[string][]byte `json:"fields"`
	}{
		Version:    Version,
		StoreState: StorePlainsight,
		Name:       ss.Name,
		Fields:     encfields,
	})

	if err != nil {
		return nil, err
	}

	// apply zlib compression
	var buf bytes.Buffer
	gz := zlib.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}

	// finalize encoded stream for return
	res := base64.StdEncoding.EncodeToString(buf.Bytes())
	return []byte(res), nil
}
