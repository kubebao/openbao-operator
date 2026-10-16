ARG OPENBAO_BASE_IMAGE=openbao/openbao:2.6.4@sha256:cf2340fc9a22cb9358ca0defd1f39b65673836bd23fe2bb8984a07e11fe13ef4
ARG PKCS11_PLUGIN_IMAGE=ghcr.io/openbao/openbao-plugin-kms-pkcs11:v0.1.0@sha256:ba3229ef91ab1040122d1389f9d015f3f211076835cf6914977c09d3697b4697
FROM ${PKCS11_PLUGIN_IMAGE} AS pkcs11-plugin
FROM ${OPENBAO_BASE_IMAGE}

COPY --from=pkcs11-plugin --chmod=0755 /openbao-plugin-kms-pkcs11 /openbao/plugins/openbao-plugin-kms-pkcs11

USER root

RUN apk add --no-cache opensc softhsm

COPY softhsm2.conf /etc/softhsm2.conf
COPY bao-softhsm-wrapper /usr/local/bin/bao-softhsm-wrapper

ENV SOFTHSM2_CONF=/etc/softhsm2.conf

RUN set -eux; \
    mkdir -p /opt/openbao-softhsm/tokens; \
    printf 'directories.tokendir = /opt/openbao-softhsm/tokens\nobjectstore.backend = file\nlog.level = ERROR\nslots.removable = false\n' > /opt/openbao-softhsm/softhsm2.conf; \
    SOFTHSM2_CONF=/opt/openbao-softhsm/softhsm2.conf \
      softhsm2-util --init-token --free --label OpenBao --so-pin 1234 --pin 1234; \
    : "SoftHSM reliably exercises OpenBao's PKCS#11 path through RSA OAEP; AES-GCM can abort in the SoftHSM library on some builds."; \
    SOFTHSM2_CONF=/opt/openbao-softhsm/softhsm2.conf \
      pkcs11-tool --module /usr/lib/softhsm/libsofthsm2.so \
        --token-label OpenBao \
        --login \
        --pin 1234 \
        --keypairgen \
        --key-type rsa:4096 \
        --label bao-root-key-rsa \
        --id 01; \
    bao_path="$(command -v bao)"; \
    mv "${bao_path}" /usr/local/bin/bao.real; \
    ln -s /usr/local/bin/bao-softhsm-wrapper "${bao_path}"; \
    chmod 0755 /usr/local/bin/bao-softhsm-wrapper; \
    sha256sum /openbao/plugins/openbao-plugin-kms-pkcs11 > /opt/openbao-softhsm/plugin.sha256; \
    chown -R 100:1000 /opt/openbao-softhsm; \
    chmod -R u=rwX,g=rX,o= /opt/openbao-softhsm

USER 100:1000
