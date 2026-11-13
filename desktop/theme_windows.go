// SPDX-License-Identifier: MIT

package main

import "golang.org/x/sys/windows/registry"

// systemPrefersDark reads the per-user "apps use light theme" switch Windows keeps.
func systemPrefersDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	light, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && light == 0
}
