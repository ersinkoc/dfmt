package cli

import "embed"

// hookFilesFS embeds the shell/git hook scripts shipped with dfmt so the
// `dfmt install-hooks` and `dfmt shell-init` commands work regardless of the
// process working directory.
//
//go:embed hooks
var hookFilesFS embed.FS
