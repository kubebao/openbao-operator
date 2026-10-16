# PyKMIP E2E fixture

The Dockerfile pins the Python base image by manifest digest. Make reads this
default from the Dockerfile. Dependabot checks this directory for Docker and
Python dependency updates.

`requirements.in` selects the PyKMIP version. `requirements.txt` pins its runtime
dependencies and artifact hashes. PyKMIP publishes only a source distribution,
so `requirements-build.txt` also pins the build tools. The image build disables
build isolation to prevent an additional, unpinned build-tool download.

To update the locks, run these commands from the repository root with `uv`:

```sh
uv pip compile --python-version 3.11 --universal --generate-hashes --no-emit-index-url \
  test/e2e/images/pykmip-server/requirements.in \
  --output-file test/e2e/images/pykmip-server/requirements.txt
uv pip compile --python-version 3.11 --universal --generate-hashes --no-emit-index-url \
  test/e2e/images/pykmip-server/requirements-build.in \
  --output-file test/e2e/images/pykmip-server/requirements-build.txt
make docker-build-e2e-pykmip-server
```

Run the KMIP E2E suite after updating the locks. The build checks hashes and
dependency consistency, but it does not verify the KMIP protocol behavior.
