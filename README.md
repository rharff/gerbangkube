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

Bagian ini membutuhkan akses administrator ke cluster, `kubectl`, Helm, Docker
atau BuildKit, serta akses ke registry image.

### Persiapan cert-manager

Pasang cert-manager melalui Helm sebelum menerapkan
`deploy/10-certificate.yaml`:

```bash
helm repo add jetstack https://charts.jetstack.io
helm repo update
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --set crds.enabled=true
kubectl -n cert-manager wait --for=condition=Available \
  deployment/cert-manager \
  deployment/cert-manager-cainjector \
  deployment/cert-manager-webhook \
  --timeout=120s
```

Pastikan release `cert-manager` berstatus `deployed` sebelum melanjutkan.

### Persiapan image controller

Build image dari root repository, beri tag sesuai registry yang akan digunakan,
lalu push image tersebut:

```bash
export CONTROLLER_IMAGE=ghcr.io/<org>/gerbangkube:0.1.0
docker build -t "$CONTROLLER_IMAGE" .
docker push "$CONTROLLER_IMAGE"
```

Ubah field `spec.template.spec.containers[0].image` pada
`deploy/30-deployment.yaml` agar sama dengan nilai `CONTROLLER_IMAGE`.
Registry harus dapat diakses oleh seluruh node k3s. Untuk cluster single-node,
image juga dapat dimuat langsung ke containerd:

```bash
docker save "$CONTROLLER_IMAGE" | sudo k3s ctr images import -
```

Untuk cluster multi-node, gunakan registry bersama; import image hanya pada
satu node tidak cukup.

### Memberikan credential registry ke gerbangkube

`docker login` hanya menyimpan credential di mesin lokal, biasanya pada
`~/.docker/config.json`. Credential tersebut belum otomatis tersedia di Pod
Kubernetes. Salin konfigurasi Docker itu ke Secret pada namespace
`gerbangkube-system`:

```bash
kubectl apply -f deploy/00-namespace.yaml

# Jalankan docker login terlebih dahulu jika belum dilakukan.
docker login ghcr.io

kubectl -n gerbangkube-system create secret generic ghcr-read-auth \
  --from-file=config.json="$HOME/.docker/config.json" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Untuk GHCR, gunakan token read-only dengan scope `read:packages`. Hindari
menaruh token langsung di file manifest atau command yang tersimpan di shell
history. Jika `config.json` berisi credential registry lain, buat file khusus
yang hanya memuat entry `ghcr.io` sebelum membuat Secret.

Konfigurasi mount Secret dan flag `--docker-config-dir=/registry-auth` sudah
tersedia langsung di [deploy/30-deployment.yaml](/home/rahan/Kuliah/Eksperimen/gerbangkube/deploy/30-deployment.yaml).
Setelah Secret dibuat, terapkan Deployment:

```bash
kubectl apply -f deploy/30-deployment.yaml
kubectl -n gerbangkube-system rollout status deploy/gerbangkube
```

Secret `ghcr-read-auth` dipakai gerbangkube untuk mengambil manifest, referrer,
dan bundle signature. Secret ini berbeda dari `imagePullSecrets` milik
workload. Jika workload juga memerlukan credential untuk menarik image private,
buat atau pasang `imagePullSecrets` pada namespace/workload tersebut secara
terpisah.

Periksa bahwa Secret sudah tersedia dan Pod berhasil membaca mount-nya:

```bash
kubectl -n gerbangkube-system get secret ghcr-read-auth
kubectl -n gerbangkube-system describe pod -l app=gerbangkube
kubectl -n gerbangkube-system logs deploy/gerbangkube
```

### Pemeriksaan jaringan dan RBAC

Sebelum memasang `ValidatingWebhookConfiguration`, pastikan:

- API server dapat mencapai Service `gerbangkube` pada port 443.
- Pod gerbangkube dapat melakukan koneksi HTTPS keluar ke registry.
- Firewall node tidak memblokir trafik dari CIDR Pod dan Service k3s.
- ServiceAccount gerbangkube dapat membaca `ImageSignaturePolicy`.
- ServiceAccount gerbangkube hanya membaca Secret pada namespace
  `gerbangkube-system`.

Setelah resource dasar diterapkan, verifikasi identitas dan permission:

```bash
kubectl auth can-i list imagesignaturepolicies \
  --as=system:serviceaccount:gerbangkube-system:gerbangkube
kubectl auth can-i list secrets -n gerbangkube-system \
  --as=system:serviceaccount:gerbangkube-system:gerbangkube
kubectl auth can-i list secrets -n default \
  --as=system:serviceaccount:gerbangkube-system:gerbangkube
```

Perintah terakhir seharusnya menghasilkan `no`.

Sebelum instalasi, pastikan cert-manager berstatus `deployed`, image controller
tersedia untuk semua node, registry dapat diakses, dan public key serta pola
image pada contoh sudah diganti.

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
