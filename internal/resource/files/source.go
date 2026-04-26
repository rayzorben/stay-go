package files

import (
	"strings"

	"github.com/rayzorben/stay-go/internal/config"
)

// sourceKind classifies the type of source in a FileEntry.
type sourceKind int

const (
	kindSecret   sourceKind = iota // ${secrets.*}
	kindLocal                      // filesystem path (not http/git)
	kindGitSSH                     // git@host:path.git or ssh://git@...
	kindGitHTTPS                   // https://host/path.git
	kindHTTP                       // https://host/file (non-git download)
	kindInline                     // content: field — literal file content
)

// detectKind classifies a FileEntry into one of the known source kinds.
func detectKind(entry *config.FileEntry) sourceKind {
	if entry.Content != "" {
		return kindInline
	}
	source := entry.Source
	switch {
	case strings.HasPrefix(source, "${secrets."):
		return kindSecret
	case strings.HasPrefix(source, "git@"), strings.HasPrefix(source, "ssh://git@"):
		return kindGitSSH
	case (strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")) &&
		(strings.HasSuffix(source, ".git") || strings.Contains(source, ".git/")):
		return kindGitHTTPS
	case strings.HasPrefix(source, "https://"), strings.HasPrefix(source, "http://"):
		return kindHTTP
	default:
		return kindLocal
	}
}

// secretKey extracts the key name from a "${secrets.key_name}" source string.
func secretKey(source string) string {
	return source[len("${secrets.") : len(source)-1]
}
