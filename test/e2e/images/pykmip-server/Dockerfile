ARG PYKMIP_BASE_IMAGE=python:3.11-slim@sha256:e88e9763f943ec1834f992a4b51e0f24500486803e8bc534e5767af9ea65f6ce
FROM ${PYKMIP_BASE_IMAGE}

ENV PYTHONDONTWRITEBYTECODE=1

COPY requirements-build.txt requirements.txt /tmp/

RUN python -m pip install --no-cache-dir --only-binary=:all: --require-hashes -r /tmp/requirements-build.txt && \
    python -m pip install --no-cache-dir --no-build-isolation --only-binary=:all: --no-binary=PyKMIP \
      --require-hashes -r /tmp/requirements.txt && \
    python -m pip check && \
    rm /tmp/requirements-build.txt /tmp/requirements.txt && \
    mkdir -p /var/lib/pykmip && \
    chown -R 1000:1000 /var/lib/pykmip

COPY pykmip-test-server /usr/local/bin/pykmip-test-server

RUN chmod 0555 /usr/local/bin/pykmip-test-server

USER 1000:1000

EXPOSE 5696
ENTRYPOINT ["/usr/local/bin/pykmip-test-server"]
