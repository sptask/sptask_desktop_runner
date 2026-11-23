# 🖥️ Spartask Desktop Runner

Spartask Desktop Runner, yerel bilgisayarınızda (Windows, macOS, Linux) arka planda sessizce çalışan ve **Spartask Bulut Otomasyonları** ile güvenli WebSocket tüneli üzerinden haberleşen hafif bir Go masaüstü asistanıdır.

---

## ✨ Temel Yetenekler

- **Yerel Klasör İzleme (Folder Watcher):** Belirlenen klasöre (`Spartask_Gelen_Kutusu` veya özel klasör) bırakılan dosyaları anında algılar ve buluta `desktop.file_created` olayını iletir.
- **Masaüstü Eylemleri (Actions):** Buluttaki iş akışlarının yerel makinenizde dosya okumasını (`file.read`), yazmasını (`file.write`), taşımasını/arşivlemesini (`file.move`) ve yerel bildirim göstermesini (`notify`) sağlar.
- **Tek Tıkla Eşleştirme (Browser Pairing):** Karmaşık API anahtarlarına gerek kalmadan varsayılan tarayıcınızda açılan onay ekranıyla hesabınıza bağlanır.
- **Otomatik Başlangıç (Autostart):** İsteğe bağlı olarak bilgisayar açılışında otomatik başlar.
- **Sıfır Kurulum & Bağımsız Binary:** CGO veya dış DLL bağımlılığı yoktur, tek bir çalıştırılabilir dosya olarak çalışır.
- **Tarayıcı İş Kaydı ve Oynatma (`browser.record.start`, `browser.replay`, `browser.stop`):** Kullanıcının tarayıcıda yaptığı işi adım listesi olarak kaydeder ve süreçler içinde yapay zeka kullanmadan aynı adımlarla tekrarlar. Bilgisayarda kurulu Chrome veya Edge kullanılır (ek indirme yok). Kayıt ve oynatma runner'a ait ayrı bir tarayıcı profilini (`%APPDATA%\Spartask\browser-profile`) paylaşır; şifre alanlarının değerleri bilgisayardan hiç çıkmaz; bot yalnızca kayıtta izin verilen sitelere gidebilir.

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

> **Not (Windows SmartScreen):** Windows ilk kez çalıştırırken *"Windows kişisel bilgisayarınızı korudu"* uyarısı verebilir. Açılan pencerede **Ek Bilgi (More Info)** -> **Yine de Çalıştır (Run Anyway)** butonuna tıklayarak uygulamayı güvenle başlatabilirsiniz.

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

# Kayıt/oynatma için belirli bir Chrome/Edge kullanma:
./sptask-runner --browser-path="C:\Program Files\Google\Chrome\Application\chrome.exe"

# Geliştirme: eşleştirme olmadan kayıt al (pencere kapanınca dosyaya yazar) ve oynat
./sptask-runner --record=https://ornek.com/giris --out=kayit.json
./sptask-runner --replay=kayit.json [--headless]

# Ayrı bir veri klasörüyle çalışma (cihaz bilgisi, tarayıcı profili, giden kutusu):
./sptask-runner --data-dir="C:\Test\runner"

# Test: runner tarayıcısını yalnızca 127.0.0.1 üzerinde hata ayıklama portuyla açma
./sptask-runner --browser-debug-port=9333
```

> Yeni (boş) bir `--data-dir` cihaz bilgisi taşımadığı için her seferinde yeniden eşleştirme ister ve web
> panelinde yeni bir cihaz olarak görünür. Testlerde biriken eski cihazları **Masaüstü Asistanları (Desktop Runner)**
> ekranından silin. Hata ayıklama portu tarayıcının tam denetimini verir: yalnızca test makinesinde kullanın.

### 3. Masaüstü programlarına veri girme (Windows)

Runner, Windows UI Automation ile herhangi bir programın (eczane, muhasebe, ERP istemcisi…) penceresindeki
alanlara yazabilir, düğmelerine basabilir, listelerinden seçim yapabilir ve metin okuyabilir. Runner'da hiçbir
programa özel kod yoktur: hangi pencerenin hangi alanına ne yazılacağı süreçten gelen kayıttadır.

Güvenlik kuralları:

- Bot yalnızca **bu bilgisayarda onaylanmış** programlarda çalışır. Onayı sunucu veremez; kaydın izinli
  program listesi bu onayı yalnızca daraltabilir.
- Komut kabukları, betik motorları, Dosya Gezgini, Görev Yöneticisi, yönetim konsolları, oturum/UAC
  pencereleri, tarayıcılar ve runner'ın kendisi hiçbir zaman onaylanamaz. Yönetici olarak çalışan pencerelere
  dokunulmaz.
- Hiçbir adım program başlatmaz; program açık değilse adım `APP_NOT_RUNNING` ile durur.
- Değerler UI Automation ile verilir ve geri okunarak doğrulanır. Gerçek tuş gerekirse önce hedef programın
  klavyeyi aldığı kontrol edilir.
- Bot çalışırken ekranda AI çalışanın renkli imleci, adı ve yaptığı adım görünür. Bilgisayarın başındaki kişi
  fareye tıklar, bir tuşa ya da **Shift+Esc**'ye basarsa iş `STOPPED_BY_USER` ile durur.
- Bilgisayar kilitliyse iş `WORKSTATION_LOCKED` ile durur.
- Bir adım başarısız olursa sonuçla birlikte o programın penceresinin görüntüsü gider (aynı programın öndeki hata
  kutusu varsa onun). Görüntüyü pencere kendisi çizer: üstünü örten başka programlar ve ekranın geri kalanı
  görüntüye girmez. Program kapalıysa, onaysızsa ya da bilgisayar kilitliyse görüntü alınmaz.

Göster-öğret (kayıt): süreç tasarımında masaüstü kaydı başlatılınca kişi işi onaylı programda bir kez yapar,
bitince **Shift+Esc**'ye basar. Ekranın sağ altında kırmızı "● Kaydediliyor" rozeti görünür.

- Yalnızca kaydın izinli program listesinde olan **ve** bu bilgisayarda onaylanmış programlar kaydedilir;
  başka pencerelerde yapılanlar alınmaz.
- Alan değerleri, odak alandan çıkınca UI Automation ile okunur; tuş vuruşları kaydedilmez. Şifre alanları
  hiç okunmaz (uyarı olarak bildirilir, süreçte parametre olarak eklenir).
- Düğme, onay kutusu, sekme, liste satırı gibi tıklanabilir kontrollere tıklamalar, Enter ve Escape adım olur.
  Kısayollar, F tuşları, menüler ve sağ tık kaydedilmez; kayıtlı programda yapıldıysa uyarı olarak döner.
  Listede olup bu bilgisayarda onaylı olmayan programdaki işler de uyarıyla bildirilir.
- Her kontrol için penceresinde tek olduğu doğrulanan seçiciler (automation id, ad, sınıf) ve yapısal yol
  yazılır. Kayıt en fazla 2 saat sürer (varsayılan 30 dakika).

```bash
# Programı onayla / onayı kaldır / onaylıları listele (yalnızca bu bilgisayarda)
./sptask-runner --approve-app="C:\Program Files\Eczane\eczane.exe"
./sptask-runner --revoke-app="C:\Program Files\Eczane\eczane.exe"
./sptask-runner --list-apps

# Geliştirme: eşleştirme olmadan onaylı programlarda kayıt al (Shift+Esc ile biter, --out dosyasına yazar)
./sptask-runner --desktop-record=eczane.exe --out=masaustu-kaydi.json

# Geliştirme: eşleştirme olmadan bir masaüstü kaydını oynat
./sptask-runner --desktop-replay=masaustu-kaydi.json

# Geliştirici testleri: küçük bir test penceresi açılır; kayıt testleri fareyi oynatır ve yazı yazar
SPTASK_DESKTOP_TESTS=1 go test ./internal/desktopbot/
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
