# gerbangkube

Admission controller minimal untuk menolak Pod yang menggunakan image tercakup
`ImageSignaturePolicy` tetapi tidak memiliki signature Cosign v3 yang valid.

## Build dan test

```bash
go test ./...
docker build -t ghcr.io/<org>/gerbangkube:0.1.0 .
```

Verifier membaca Sigstore bundle v0.3 dari OCI Referrers API dan mencoba tag
fallback `sha256-<digest>`. Verifikasi hanya menerima signature DSSE yang:

- memakai payload type `application/vnd.in-toto+json`;
- memiliki predicate type `https://sigstore.dev/cosign/sign/v1`;
- mengikat subject ke digest image yang baru di-resolve; dan
- lolos verifikasi ECDSA P-256 dengan public key dari policy.

Tidak ada akses Rekor, TUF, TSA, atau keyless verification.

## Instalasi di k3s

Prasyarat: cert-manager telah terpasang dan image controller tersedia pada
registry yang dapat diakses semua node.

```bash
kubectl apply -f deploy/00-namespace.yaml
kubectl apply -f deploy/05-crd.yaml
kubectl apply -f deploy/06-rbac.yaml
kubectl apply -f deploy/10-certificate.yaml
kubectl apply -f examples/secret.yaml
kubectl apply -f deploy/30-deployment.yaml
kubectl -n gerbangkube-system rollout status deploy/gerbangkube
kubectl apply -f deploy/40-webhook.yaml
kubectl apply -f examples/policy.yaml
```

Ganti public key dan pola `ghcr.io/example/*` pada contoh sebelum digunakan.
Untuk registry private, mount Secret Docker config ke Deployment dan jalankan
controller dengan `--docker-config-dir=/registry-auth`; token hanya perlu
memiliki hak baca package.

Policy dan Secret dimuat ulang setiap 10 detik, sehingga perubahan berlaku tanpa
restart. Cache hanya menyimpan verifikasi sukses selama 10 menit; kegagalan
tidak di-cache. Jika controller tidak tersedia, webhook menggunakan
`failurePolicy: Fail` dan Pod baru di namespace tercakup ditolak.

Untuk pemulihan darurat:

```bash
kubectl delete validatingwebhookconfiguration gerbangkube
```

## Cakupan versi 1

Webhook memproses `CREATE` Pod dan memeriksa init container, container biasa,
serta ephemeral container yang hadir dalam objek request. Resource lain,
rekonsiliasi Pod yang sudah berjalan, `requireDigest`, status CRD, metrics, dan
mutating webhook belum termasuk.
