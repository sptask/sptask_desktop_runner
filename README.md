# 🖥️ Spartask Desktop Runner

Spartask Desktop Runner, yerel bilgisayarınızda (Windows, macOS, Linux) arka planda sessizce çalışan ve **Spartask Bulut Otomasyonları** ile güvenli WebSocket tüneli üzerinden haberleşen hafif bir Go masaüstü asistanıdır.

---

## ✨ Temel Yetenekler

- **Yerel Klasör İzleme (Folder Watcher):** Belirlenen klasöre (`Spartask_Gelen_Kutusu` veya özel klasör) bırakılan dosyaları anında algılar ve buluta `desktop.file_created` olayını iletir.
- **Masaüstü Eylemleri (Actions):** Buluttaki iş akışlarının yerel makinenizde dosya okumasını (`file.read`), yazmasını (`file.write`), taşımasını/arşivlemesini (`file.move`) ve yerel bildirim göstermesini (`notify`) sağlar.
- **Tek Tıkla Eşleştirme (Browser Pairing):** Karmaşık API anahtarlarına gerek kalmadan varsayılan tarayıcınızda açılan onay ekranıyla hesabınıza bağlanır.
- **Otomatik Başlangıç (Autostart):** İsteğe bağlı olarak bilgisayar açılışında otomatik başlar.
- **Sıfır Kurulum & Bağımsız Binary:** CGO veya dış DLL bağımlılığı yoktur, tek bir çalıştırılabilir dosya olarak çalışır.

---

## 📥 İndirme Linkleri (Resmi Sürümler)

En son sürümleri doğrudan aşağıdaki bağlantılardan indirebilirsiniz:

- 🪟 **Windows x64 (.exe):**  
  [`sptask-runner-windows-amd64.exe`](https://github.com/sptask/sptask_desktop_runner/releases/latest/download/sptask-runner-windows-amd64.exe)
- 🍏 **macOS Apple Silicon (M1/M2/M3):**  
  [`sptask-runner-darwin-arm64.zip`](https://github.com/sptask/sptask_desktop_runner/releases/latest/download/sptask-runner-darwin-arm64.zip)
- 🍏 **macOS Intel (x64):**  
  [`sptask-runner-darwin-amd64.zip`](https://github.com/sptask/sptask_desktop_runner/releases/latest/download/sptask-runner-darwin-amd64.zip)
- 🐧 **Linux x64 (.tar.gz):**  
  [`sptask-runner-linux-amd64.tar.gz`](https://github.com/sptask/sptask_desktop_runner/releases/latest/download/sptask-runner-linux-amd64.tar.gz)

---

## 🚀 Başlarken

### 1. Çalıştırma
İndirdiğiniz dosyaya çift tıklayarak veya terminalden çalıştırın:

```bash
# Windows
.\sptask-runner-windows-amd64.exe

# Linux / macOS
chmod +x sptask-runner
./sptask-runner
```

### 2. Komut Satırı Parametreleri
```bash
# Özel sunucu veya web paneline bağlanma (Geliştirme ortamı için):
./sptask-runner --server=https://api.spartask.com --frontend=https://app.spartask.com

# Özel bir izleme klasörü tanımlama:
./sptask-runner --watch-dir="C:\Muhasebe\Gelenler"

# Windows başlangıcında otomatik çalışmayı açma/kapatma:
./sptask-runner --autostart=enable
./sptask-runner --autostart=disable

# Cihaz bağlantısını sıfırlayıp baştan eşleştirme:
./sptask-runner --reset

# Versiyon kontrolü:
./sptask-runner --version
```

---

## 🛠️ Kaynak Koddan Derleme

```bash
git clone https://github.com/sptask/sptask_desktop_runner.git
cd sptask_desktop_runner
go build -o sptask-runner .
```

---

## 📄 Lisans
Bu proje Spartask ekosisteminin bir parçasıdır.
