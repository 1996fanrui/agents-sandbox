.PHONY: test integration-test

# Unit tests + mock tests. No daemon, no root, no Docker required.
test:
	go test ./...
	cd sdk/python && uv run pytest tests/ --ignore=tests/test_real_runtime.py --ignore=tests/test_network_isolation.py

# Integration tests requiring a real daemon with CAP_NET_ADMIN.
# CAP_NET_ADMIN is granted to the test daemon binary by the test setup via sudo -n setcap.
integration-test:
	cd sdk/python && AGBOX_REQUIRE_INTEGRATION=1 uv run pytest tests/test_real_runtime.py tests/test_network_isolation.py -v
