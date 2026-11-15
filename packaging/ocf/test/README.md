Offline tests for the `ocf:webkvm:incus` agent: `sh packaging/ocf/test/run.sh` (add `-v` to see agent logs on failure).
`incus`, `timeout`, `sleep`, `date` and `ocf-shellfuncs` are fakes (`fake-*`) placed in a temp PATH/OCF_ROOT; nothing real is touched.
Exit code is non-zero if any scenario fails; a PASS/FAIL summary is printed at the end.
