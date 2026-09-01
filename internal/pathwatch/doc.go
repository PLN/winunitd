// Package pathwatch parses PathChanged= values and watches files/directories.
//
// Paths are absolute Windows paths only (DESIGN.md §6.4 / §39). Repeatable
// PathChanged= is OR: any watched path firing starts the counterpart.
// Watches use ReadDirectoryChangesW and are non-recursive (this directory
// only). A file path watches the parent directory and filters by name.
// Linux builds stub OpenWatch so parse/verify and protocol tests stay portable.
package pathwatch
