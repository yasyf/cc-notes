//go:build darwin

package cli

import "github.com/yasyf/cc-notes/internal/helperpackage"

var (
	installPackage   = helperpackage.Install
	uninstallPackage = helperpackage.Uninstall
)
