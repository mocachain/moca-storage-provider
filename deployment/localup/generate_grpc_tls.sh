#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 <output-directory> <identity>" >&2
  exit 1
fi

output_dir=$1
identity=$2
ca_key_file="${output_dir}/ca.key"
ca_cert_file="${output_dir}/ca.crt"
cert_file="${output_dir}/${identity}.crt"
key_file="${output_dir}/${identity}.key"
csr_file="${output_dir}/${identity}.csr"
extension_file="${output_dir}/${identity}.ext"

mkdir -p "${output_dir}"

if [ ! -f "${ca_key_file}" ] || [ ! -f "${ca_cert_file}" ]; then
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${ca_key_file}" \
    -out "${ca_cert_file}" \
    -days 2 \
    -subj "/CN=moca-sp local E2E CA" >/dev/null 2>&1
fi

openssl req -newkey rsa:2048 -nodes \
  -keyout "${key_file}" \
  -out "${csr_file}" \
  -subj "/CN=${identity}" >/dev/null 2>&1

{
  echo "subjectAltName=IP:127.0.0.1,URI:spiffe://mocachain.local/${identity}"
  echo "extendedKeyUsage=serverAuth,clientAuth"
} >"${extension_file}"

openssl x509 -req \
  -in "${csr_file}" \
  -CA "${ca_cert_file}" \
  -CAkey "${ca_key_file}" \
  -CAcreateserial \
  -out "${cert_file}" \
  -days 2 \
  -extfile "${extension_file}" >/dev/null 2>&1

rm -f "${csr_file}" "${extension_file}"
chmod 600 "${ca_key_file}" "${key_file}"

