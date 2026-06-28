# ArahRide — server Go demo

Server lokal ini menghubungkan Arah Customer dan Arah Driver dari [Android](../Android/ArahRide/README.md) maupun [iOS](../IOS/ArahRide/README.md). Ini **bukan backend produksi**: satu Driver, satu pesanan aktif, memori proses saja, tanpa akun atau otorisasi. Default hanya mendengar di `127.0.0.1:18080`.

## Jalankan

Memerlukan Go 1.23 atau lebih baru (`brew install go` di macOS).

```sh
go mod download
go run .
```

Untuk perangkat fisik di LAN pengembangan:

```sh
ARAH_ADDR=0.0.0.0:18080 go run .
```

Jangan buka port server demo ke internet. Untuk verifikasi: `go test ./...` dan `curl http://127.0.0.1:18080/health`.

## Kontrak prototipe

- `GET /v1/state` → snapshot terkini.
- `POST /v1/actions` → satu aksi dengan JSON `role`, `action`, dan titik opsional.
- `GET /v1/events?role=customer|driver` → WebSocket yang mengirim snapshot awal dan setiap perubahan.

Status: `requested`, `accepted`, `arrived`, `in_progress`, `completed`, `cancelled`.
Aksi Customer: `create` (butuh `pickup` dan `dropoff`), `cancel`, `reset`.
Aksi Driver: `online`, `offline`, `accept`, `arrive`, `start`, `complete`, `location` (butuh `location`).
Titik menggunakan JSON `{"latitude":-6.2088,"longitude":106.8456}`. Snapshot berisi `sequence`, `driverOnline`, `driverLocation`, dan `ride` (atau `null`). `sequence` memungkinkan aplikasi mengabaikan update yang lebih lama saat HTTP dan WebSocket tiba dengan urutan berbeda.

Saat Customer membuat pesanan, server mengambil geometri jalan OpenStreetMap dari OSRM (`ARAH_ROUTING_URL`, default `https://router.project-osrm.org`) dan menyimpannya pada pesanan. Aksi Driver `start` memicu backend mengirim posisi simulasi setiap 100 ms selama 16 detik mengikuti geometri rute itu. Kedua aplikasi menggambar geometri yang sama sebagai polyline dan menggerakkan ikon mobil di atasnya. Aplikasi menghentikan pembaruan GPS perangkat selama status `in_progress` agar tidak menimpa simulasi.

Endpoint OSRM publik hanya untuk demo dan dapat membatasi pemakaian. Untuk produksi, host OSRM sendiri atau pilih layanan routing ber-SLA; lokasi jemput dan tujuan dikirim ke endpoint routing yang dikonfigurasi.

## Pencarian tempat (Photon)

- `GET /v1/places/search?q=<kata kunci>&lat=<opsional>&lon=<opsional>` → `{"results":[{"name","displayName","point"}]}`. Kata kunci 2–160 karakter; maksimal 8 hasil; `lat` dan `lon` opsional dan harus dikirim berpasangan.
- `GET /v1/places/reverse?lat=<lat>&lon=<lon>` → satu `PlaceResult`; `404` jika tidak ada kecocokan.
- Input tidak valid → `400`; upstream timeout (4 detik), non-2xx, atau JSON rusak → `502` dengan pesan generik (isi respons upstream tidak diteruskan).
- `ARAH_GEOCODER_URL` menentukan basis URL Photon. Default `https://photon.komoot.io` (demo publik) **hanya untuk pengembangan**. Produksi memerlukan layanan Photon terkelola atau self-hosted, karena teks pencarian dan titik peta dikirim ke endpoint tersebut.

- `ARAH_GEOCODER_PROVIDER` memilih penyedia: `photon` (default) atau `nominatim`. Dengan `nominatim`, `ARAH_GEOCODER_URL` default ke `https://nominatim.openstreetmap.org` (dev saja; kebijakan penggunaannya membatasi 1 request/detik dan mewajibkan User-Agent, keduanya sudah dipenuhi backend). Hasil dibatasi ke Indonesia dan label RT/RW/kode pos dibuang. API publik Photon (`photon.komoot.io/api`) dapat tidak merespons; pakai `ARAH_GEOCODER_PROVIDER=nominatim` untuk pengembangan, atau Photon sendiri.

Contoh Nominatim: `ARAH_GEOCODER_PROVIDER=nominatim go run .`

Contoh lokal dengan Photon sendiri: `ARAH_GEOCODER_URL=http://127.0.0.1:2322 go run .`

Contoh siklus tanpa aplikasi:

```sh
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"driver","action":"online"}'
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"customer","action":"create","pickup":{"latitude":-6.2088,"longitude":106.8456},"dropoff":{"latitude":-6.1754,"longitude":106.8272}}'
curl -X POST http://127.0.0.1:18080/v1/actions -H 'Content-Type: application/json' -d '{"role":"driver","action":"accept"}'
```

## Rekomendasi backend Go produksi

Gunakan **HTTPS untuk tindakan dan WebSocket/WSS untuk update perjalanan ke aplikasi**. WebSocket adalah koneksi dua arah langsung ke perangkat; RabbitMQ adalah broker antarlayanan, bukan pengganti koneksi realtime ke aplikasi. Saat satu proses Go cukup, broadcast lokal seperti demo ini paling sederhana. Saat menjadi beberapa instance, gunakan Redis Pub/Sub atau broker serupa untuk fan-out antargateway; simpan status perjalanan dan lokasi terakhir di PostgreSQL/PostGIS agar klien yang tersambung ulang bisa mengambil snapshot. Jika nanti ada tugas asinkron yang perlu retry dan antrean tahan gangguan (misalnya notifikasi, bukti perjalanan, atau analitik), RabbitMQ cocok untuk pekerjaan itu.

Sebelum rilis: tambah autentikasi Customer/Driver, otorisasi per pesanan dan kanal, pemilihan Driver tanpa race, idempotency key untuk aksi, simpan status durable, batas frekuensi dan validasi lokasi, heartbeat/reconnect WebSocket, push APNs/FCM saat aplikasi tidak aktif, TLS, observability, kebijakan retensi lokasi, serta pemisahan lingkungan demo dan produksi. Endpoint demo saat ini tidak memenuhi persyaratan tersebut.

## Hasil verifikasi pencarian tempat (2026-10-15)

- Otomatis: Go: `go test ./...` lulus.
- Backend dengan Photon palsu lokal (`ARAH_GEOCODER_URL`): `/v1/places/search` dan `/v1/places/reverse` memetakan hasil dengan benar; kata kunci kosong → 400.
- **Belum diverifikasi**: Photon publik (`photon.komoot.io`) tidak terjangkau dari mesin ini saat pengujian (backend mengembalikan 502 generik), sehingga pencarian alamat/POI Indonesia sungguhan dan ketersediaan produksi tidak teruji. Uji emulator/simulator end-to-end (pilih titik, buat pesanan, label di app Driver, tap peta) juga belum dijalankan.
