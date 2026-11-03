// Package seal encrypts files with a password: the configuration the app
// backs up to iCloud Drive. The password is stretched into a key with
// Argon2id (RFC 9106), and the file is encrypted and authenticated with
// XChaCha20-Poly1305; the header, which says how the key was made, is
// authenticated with it, so a file changed in any byte does not open.
//
// A sealed file:
//
//	magic    "CSBACKUP" (8 bytes)
//	version  1          (1)
//	time     Argon2id passes, big-endian (4)
//	memory   Argon2id memory in KiB, big-endian (4)
//	threads  Argon2id lanes (1)
//	salt     random (16)
//	nonce    random (24)
//	sealed   XChaCha20-Poly1305 of the content, header as additional data
package seal

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

const (
	magic   = "CSBACKUP"
	version = 1

	saltSize   = 16
	keySize    = chacha20poly1305.KeySize
	headerSize = len(magic) + 1 + 4 + 4 + 1 + saltSize + chacha20poly1305.NonceSizeX
)

// Params are how hard Argon2id works to turn a password into a key.
type Params struct {
	Time    uint32 // passes over the memory
	Memory  uint32 // KiB
	Threads uint8
}

// DefaultParams are RFC 9106's second recommendation: 64 MiB, three passes,
// four lanes; a fraction of a second on a laptop, for every guess.
var DefaultParams = Params{Time: 3, Memory: 64 * 1024, Threads: 4}

// Errors of Open.
var (
	// ErrNotSealed is data that is not a file this package sealed.
	ErrNotSealed = errors.New("not an encrypted backup")
	// ErrUnsupported is a sealed file of a newer version.
	ErrUnsupported = errors.New("the backup was made by a newer version of the app")
	// ErrWrongPassword is a password that does not open the file, or a
	// file that was changed since it was sealed: the two look the same.
	ErrWrongPassword = errors.New("wrong password, or the backup was changed")
)

// Key is a key made from a password, with the salt and the parameters that
// made it, which a sealed file carries so that the password opens it again.
type Key struct {
	params Params
	salt   [saltSize]byte
	key    [keySize]byte
}

// NewKey makes a key from password with a new random salt. It takes as
// long as DefaultParams make it: call it off the main thread.
func NewKey(password string) (*Key, error) {
	return newKey(password, DefaultParams)
}

func newKey(password string, p Params) (*Key, error) {
	k := &Key{params: p}
	if _, err := rand.Read(k.salt[:]); err != nil {
		return nil, err
	}
	k.derive(password)
	return k, nil
}

func (k *Key) derive(password string) {
	copy(k.key[:], argon2.IDKey([]byte(password), k.salt[:], k.params.Time, k.params.Memory, k.params.Threads, keySize))
}

// Seal encrypts content with a new random nonce.
func (k *Key) Seal(content []byte) ([]byte, error) {
	header := make([]byte, 0, headerSize)
	header = append(header, magic...)
	header = append(header, version)
	header = binary.BigEndian.AppendUint32(header, k.params.Time)
	header = binary.BigEndian.AppendUint32(header, k.params.Memory)
	header = append(header, k.params.Threads)
	header = append(header, k.salt[:]...)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	header = append(header, nonce...)
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, nonce, content, header), nil
}

// Sealed reports whether data looks like a sealed file.
func Sealed(data []byte) bool { return bytes.HasPrefix(data, []byte(magic)) }

// maxMemory bounds the memory a file may ask Argon2id for, 1 GiB: a file
// is not trusted to make the app use all of the computer's.
const maxMemory = 1 << 20

// Open decrypts a sealed file with password.
func Open(password string, data []byte) ([]byte, error) {
	if !Sealed(data) || len(data) < headerSize+chacha20poly1305.Overhead {
		return nil, ErrNotSealed
	}
	if data[len(magic)] != version {
		return nil, ErrUnsupported
	}
	r := data[len(magic)+1:]
	k := &Key{params: Params{
		Time:    binary.BigEndian.Uint32(r[0:4]),
		Memory:  binary.BigEndian.Uint32(r[4:8]),
		Threads: r[8],
	}}
	if k.params.Time == 0 || k.params.Time > 100 || k.params.Memory < 8 || k.params.Memory > maxMemory || k.params.Threads == 0 {
		return nil, ErrNotSealed
	}
	copy(k.salt[:], r[9:9+saltSize])
	nonce := r[9+saltSize : 9+saltSize+chacha20poly1305.NonceSizeX]
	k.derive(password)
	aead, err := chacha20poly1305.NewX(k.key[:])
	if err != nil {
		return nil, err
	}
	content, err := aead.Open(nil, nonce, data[headerSize:], data[:headerSize])
	if err != nil {
		return nil, ErrWrongPassword
	}
	return content, nil
}

// Encode writes the key, its salt and parameters as text, to keep in the
// system's credential store: it seals, as the password would, without the
// password itself being kept anywhere.
func (k *Key) Encode() string {
	b := make([]byte, 0, 1+9+saltSize+keySize)
	b = append(b, version)
	b = binary.BigEndian.AppendUint32(b, k.params.Time)
	b = binary.BigEndian.AppendUint32(b, k.params.Memory)
	b = append(b, k.params.Threads)
	b = append(b, k.salt[:]...)
	b = append(b, k.key[:]...)
	return base64.StdEncoding.EncodeToString(b)
}

// DecodeKey reads what Encode wrote.
func DecodeKey(text string) (*Key, error) {
	b, err := base64.StdEncoding.DecodeString(text)
	if err != nil || len(b) != 1+9+saltSize+keySize || b[0] != version {
		return nil, fmt.Errorf("not an encoded key")
	}
	k := &Key{params: Params{
		Time:    binary.BigEndian.Uint32(b[1:5]),
		Memory:  binary.BigEndian.Uint32(b[5:9]),
		Threads: b[9],
	}}
	copy(k.salt[:], b[10:10+saltSize])
	copy(k.key[:], b[10+saltSize:])
	return k, nil
}

// Equal reports whether two keys are the same, in constant time.
func (k *Key) Equal(o *Key) bool {
	return k.params == o.params &&
		subtle.ConstantTimeCompare(k.salt[:], o.salt[:]) == 1 && subtle.ConstantTimeCompare(k.key[:], o.key[:]) == 1
}
