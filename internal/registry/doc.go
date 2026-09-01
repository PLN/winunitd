// Package registry parses RegistryChanged= hive paths and watches keys.
//
// Hive syntax is HKLM\… or HKCU\… only (DESIGN.md §39). There is no
// PowerShell drive and no implicit PowerShell. Watches use
// RegNotifyChangeKeyValue on the key and its subtree. Linux builds
// stub OpenWatch so parse/verify and protocol tests stay portable.
package registry
