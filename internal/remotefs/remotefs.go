// Package remotefs defines the protocol-agnostic interface the UI uses to
// browse a remote filesystem. FTP, FTPS, and SFTP adapters will implement
// this same interface alongside the existing fake adapter.
package remotefs

import "tideftp/internal/domain"

type FS interface {
	List(dirPath string, showHidden bool) []domain.Entry
	Child(current, name string) string
	Parent(current string) string
}
