package db

type DB interface {
	Get(key []byte) ([]byte, error)
}
