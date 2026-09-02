// Package pathwatch parses PathChanged= / PathExists= values and watches
// files/directories.
//
// Paths are absolute Windows paths only (DESIGN.md §6.4 / §39). Repeatable
// PathChanged= is OR: any watched path firing starts the counterpart.
// Repeatable PathExists= is AND: every listed path must exist (a lock versus
// systemd, where PathExists= is OR). Watches use ReadDirectoryChangesW and
// are non-recursive (this directory only). A PathChanged= file path watches
// the parent directory and filters by name. A PathExists= path may be missing:
// the nearest existing ancestor directory is watched so creation can satisfy,
// with wakeups filtered to create/delete/rename of the next path component.
// Linux builds stub OpenWatch / OpenExistsWatch so parse/verify and protocol
// tests stay portable.
package pathwatch
