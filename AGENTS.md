# Repository privacy

Treat every tracked file, commit message, pull request, workflow log, and release artifact as potentially public.

- Do not include the operator's personal name, username, real machine names, internal domains or IP addresses, private profile paths, credentials, or infrastructure inventory.
- Use generic host roles such as `dev`, `test`, `lab`, and `prod`; use `alice`, `bob`, and `carol` for example users.
- Use `example.com` and documentation-reserved addresses for network examples. Keep loopback addresses where behavior specifically requires loopback.
- Keep actual deployment mappings, credentials, and unsanitized evidence in the private operator workspace outside this repository.
- Review the complete staged diff and any generated output for identifying information before committing or publishing. Do not put real identifiers in a committed denylist or in descriptions of a redaction.
- Removing identifying text from the current tree does not remove it from Git history. Review and sanitize historical content before public release.

Preserve functional module/repository identifiers and existing license notices when editing examples or operational documentation; do not invent replacement legal attribution.
