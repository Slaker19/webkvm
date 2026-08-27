package appliances

// provisionScripts holds the embedded bash provisioning scripts that run
// on first boot (via cloud-init) to install an application on top of a
// base cloud image. These are embedded at build time, never exposed over
// the API and never user-editable — this keeps the installation steps
// coherent and prevents an admin from injecting arbitrary commands.
//
// Each script runs as root on a fresh cloud-init provisioned VM. It
// assumes the base image already has cloud-init and a working network.
var provisionScripts = map[string]string{
	"wordpress": `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y apache2 mysql-server php php-mysql php-curl php-gd php-xml php-mbstring php-zip libapache2-mod-php wget unzip
WP_DB=wordpress
WP_USER=wpuser
# Substituted by WebKVM at deploy time so it can also be shown in the UI.
WP_PASS='{{WEBKVM_DB_PASS}}'
mysql <<SQL
CREATE DATABASE IF NOT EXISTS ${WP_DB};
CREATE USER IF NOT EXISTS '${WP_USER}'@'localhost' IDENTIFIED BY '${WP_PASS}';
GRANT ALL PRIVILEGES ON ${WP_DB}.* TO '${WP_USER}'@'localhost';
FLUSH PRIVILEGES;
SQL
cd /var/www/html
rm -f index.html
wget -q https://wordpress.org/latest.tar.gz
tar xzf latest.tar.gz
mv wordpress/* .
rmdir wordpress
rm -f latest.tar.gz
chown -R www-data:www-data /var/www/html
# Restart (not just start): apt's postinst may have auto-started Apache
# before the site config was enabled, leaving the alias unloaded.
systemctl enable apache2 >/dev/null 2>&1
systemctl restart apache2
systemctl enable --now mysql
for i in $(seq 1 30); do mysql -e "SELECT 1" >/dev/null 2>&1 && break; sleep 1; done
echo "WordPress provisioned (finish setup in the browser wizard)."
echo "===== WEBKVM CREDENTIALS ====="
VMIP=$(hostname -I | awk '{print $1}')
echo "URL: http://$VMIP/  -> finish /wp-admin/install.php"
echo "DATABASE: mysql db=$WP_DB user=$WP_USER"
echo "NOTE: change ALL passwords when done."
echo "=============================="
# Publish connection details inside the guest (bashrc + motd).
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : WordPress
 URL        : http://$VMIP/
 Estado     : termina el wizard (/wp-admin/install.php) con la BD de abajo
 Database   : mysql | db=$WP_DB | user=$WP_USER | pass=$WP_PASS
 IMPORTANTE : al terminar CAMBIA todas las contrasenas (admin y BD)
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() {
  [ -f "$1" ] || return 0
  grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"
}
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6)
[ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
`,

	"nextcloud": `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y apache2 mariadb-server php php-curl php-mysql php-xml php-mbstring php-gd php-zip php-intl php-bcmath php-gmp php-imagick libapache2-mod-php wget tar bzip2
NC_DB=nextcloud
NC_USER=ncuser
# Substituted by WebKVM at deploy time so it can also be shown in the UI.
NC_PASS='{{WEBKVM_DB_PASS}}'
mysql <<SQL
CREATE DATABASE IF NOT EXISTS ${NC_DB};
CREATE USER IF NOT EXISTS '${NC_USER}'@'localhost' IDENTIFIED BY '${NC_PASS}';
GRANT ALL PRIVILEGES ON ${NC_DB}.* TO '${NC_USER}'@'localhost';
FLUSH PRIVILEGES;
SQL
cd /var/www
wget -q https://download.nextcloud.com/server/releases/latest.tar.bz2
tar xjf latest.tar.bz2
rm -f latest.tar.bz2
chown -R www-data:www-data /var/www/nextcloud
cat > /etc/apache2/sites-available/nextcloud.conf <<CONF
Alias /nextcloud "/var/www/nextcloud/"
<Directory /var/www/nextcloud/>
  Options +FollowSymlinks
  AllowOverride All
  Require all granted
</Directory>
CONF
a2ensite nextcloud
a2enmod rewrite headers env dir mime
# Restart (not just start): apt's postinst may have auto-started Apache
# before the site config was enabled, leaving the alias unloaded.
systemctl enable apache2 >/dev/null 2>&1
systemctl restart apache2
systemctl enable --now mariadb
for i in $(seq 1 30); do mysql -e "SELECT 1" >/dev/null 2>&1 && break; sleep 1; done
# Self-healing helper: once the user finishes the web installer, keep the
# VM's current LAN IP registered as a trusted domain so Nextcloud does not
# refuse access. Runs on every boot; exits early until config.php exists.
cat > /usr/local/bin/webkvm-fix-trusted <<'FIX'
#!/bin/bash
CONF=/var/www/nextcloud/config/config.php
[ -f "$CONF" ] || exit 0
IP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$IP" ] && exit 0
cd /var/www/nextcloud
sudo -u www-data php occ config:system:set trusted_domains 2 --value="$IP" >/dev/null 2>&1 || true
exit 0
FIX
chmod +x /usr/local/bin/webkvm-fix-trusted
cat > /etc/systemd/system/webkvm-trusted-domains.service <<UNIT
[Unit]
Description=WebKVM: keep Nextcloud trusted_domains in sync with VM IP
After=network-online.target apache2.service mariadb.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/webkvm-fix-trusted

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable webkvm-trusted-domains.service >/dev/null 2>&1
echo "Nextcloud provisioned (finish setup in the browser installer)."
echo "===== WEBKVM CREDENTIALS ====="
VMIP=$(hostname -I | awk '{print $1}')
echo "URL: http://$VMIP/nextcloud/  -> finish the web installer"
echo "DATABASE: mariadb db=$NC_DB user=$NC_USER"
echo "NOTE: change ALL passwords when done."
echo "=============================="
# Publish connection details inside the guest (bashrc + motd) so anyone
# logging in immediately sees which app this VM runs and how to reach it.
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Nextcloud
 URL        : http://$VMIP/nextcloud/
 Estado     : completa el instalador web (ahi creas tu usuario admin)
 Database   : mariadb | db=$NC_DB | user=$NC_USER | pass=$NC_PASS
 IMPORTANTE : al terminar CAMBIA todas las contrasenas (admin y BD)
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() {
  [ -f "$1" ] || return 0
  grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"
}
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6)
[ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
`,

	"odoo": `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y gnupg wget postgresql
# Official Odoo nightly APT repository — the PyPI "odoo" name publishes no
# distributions, so pip/venv installs are impossible.
# [trusted=yes] evita depender de la descarga de la clave GPG (red
# intermitente rompía el provisioning); el repo es oficial de Odoo.
curl -fsSL --retry 3 https://nightly.odoo.com/keys/odoo.key | gpg --dearmor -o /usr/share/keyrings/odoo-archive-keyring.gpg || true
echo "deb [trusted=yes] http://nightly.odoo.com/18.0/nightly/deb/ ./" > /etc/apt/sources.list.d/odoo.list
apt-get update -y
apt-get install -y odoo
# --- Ubuntu 24.04 (noble) compatibility fixes for the Odoo nightly ---
apt-get install -y python3-pip python3-lxml-html-clean >/dev/null 2>&1 || true
pip3 install --break-system-packages -q lxml-html-clean 2>/dev/null || true
# lxml 5.x split html.clean into a separate project and dropped the legacy
# defs API that odoo/tools/mail.py still consumes; restore it.
grep -q WEBKVM_DEFS_SHIM /usr/lib/python3/dist-packages/lxml/html/clean.py || cat >> /usr/lib/python3/dist-packages/lxml/html/clean.py <<SHIM

# --- WEBKVM_DEFS_SHIM: restore legacy lxml.html.clean.defs API ---
import sys as _sys, types as _types
_defs = _types.ModuleType("lxml.html.clean.defs")
_defs.inline_tags = frozenset(("abbr","acronym","b","big","cite","code","del","em","font","i","img","input","ins","kbd","label","nobr","q","s","samp","select","small","span","strike","strong","sub","sup","textarea","tt","u","var"))
_defs.block_tags = frozenset(("address","blockquote","button","center","dd","dir","div","dl","dt","fieldset","form","frameset","h1","h2","h3","h4","h5","h6","hr","iframe","isindex","li","map","menu","noframes","noscript","object","ol","optgroup","p","pre","script","select","table","td","th","tr","ul"))
_defs.empty_tags = frozenset(("area","base","basefont","br","col","embed","frame","hr","img","input","isindex","keygen","link","meta","param","source","track","wbr"))
_defs.safe_attrs = frozenset(("abbr","accept","accept-charset","accesskey","action","align","alt","autocomplete","axis","background","bgcolor","border","cellpadding","cellspacing","char","charoff","charset","checked","class","clear","cols","colspan","compact","content","coords","datetime","dir","disabled","enctype","for","frame","headers","height","hreflang","hspace","id","ismap","label","lang","language","longdesc","maxlength","media","method","multiple","name","nohref","noshade","nowrap","pattern","readonly","rel","rev","rows","rowspan","rules","scope","selected","shape","size","span","start","summary","tabindex","target","title","type","usemap","valign","value","vspace","width"))
from lxml.html.clean import Cleaner as _WC
try:
    _defs.safe_attrs |= frozenset(_WC().safe_attrs)
except Exception:
    pass
_defs.tags = _defs.block_tags | _defs.inline_tags | _defs.empty_tags | frozenset(("a","applet","area","article","aside","audio","base","bdi","bdo","body","canvas","caption","command","datalist","details","dialog","embed","figcaption","figure","footer","form","frame","frameset","head","header","hgroup","html","iframe","legend","link","main","mark","menuitem","meta","meter","nav","noframes","noscript","object","optgroup","option","output","param","progress","rp","rt","ruby","section","source","style","summary","table","tbody","td","template","tfoot","thead","th","time","title","tr","video"))
_sys.modules["lxml.html.clean.defs"] = _defs
globals()["defs"] = _defs
SHIM
# Python 3.12 emits RETURN_CONST for constant returns; odoo's QWeb
# safe-opcode whitelist predates it.
sed -i "s/'RETURN_VALUE', # return the result/'RETURN_CONST', 'RETURN_VALUE', # return the result/" /usr/lib/python3/dist-packages/odoo/tools/safe_eval.py
# werkzeug 3.x removed werkzeug.urls.URL and changed set_cookie internals.
rm -rf /usr/local/lib/python3*/dist-packages/werkzeug* 2>/dev/null || true
pip3 install --break-system-packages -q --ignore-installed "werkzeug==2.2.3"
sudo -u postgres psql -c "CREATE USER odoo WITH PASSWORD 'odoo' CREATEDB;" 2>/dev/null || sudo -u postgres psql -c "ALTER USER odoo WITH PASSWORD 'odoo' CREATEDB;"
systemctl daemon-reload
systemctl enable --now postgresql >/dev/null 2>&1 || true
sleep 3
systemctl enable --now odoo
# Publish connection details inside the guest (bashrc + motd).
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Odoo
 URL        : http://$VMIP:8069/
 Admin      : se define al crear la base de datos (primer acceso)
 Database   : postgresql | user=odoo | pass=odoo
 IMPORTANTE : al terminar CAMBIA el master password y las contrasenas
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() {
  [ -f "$1" ] || return 0
  grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"
}
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6)
[ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Odoo provisioned on port 8069."
`,

	"moodle": `#!/bin/bash
set -e
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y apache2 mariadb-server php-soap php php-mysql php-xml php-mbstring php-gd php-zip php-intl php-curl php-xmlrpc libapache2-mod-php wget tar
# Substituted by WebKVM at deploy time so it can also be shown in the UI.
MOODLE_PASS='{{WEBKVM_DB_PASS}}'
mysql <<SQL
CREATE DATABASE IF NOT EXISTS moodle DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE USER IF NOT EXISTS 'moodle'@'localhost' IDENTIFIED BY '${MOODLE_PASS}';
GRANT ALL PRIVILEGES ON moodle.* TO 'moodle'@'localhost';
FLUSH PRIVILEGES;
SQL
mkdir -p /var/www/moodle /var/moodledata
cd /var/www
wget -q https://download.moodle.org/download.php/direct/stable405/moodle-latest-405.tgz
tar xzf moodle-latest-405.tgz
rm -f moodle-latest-405.tgz
chown -R www-data:www-data /var/www/moodle /var/moodledata
cat > /etc/apache2/sites-available/moodle.conf <<CONF
Alias /moodle "/var/www/moodle/"
<Directory /var/www/moodle/>
  Options FollowSymLinks
  AllowOverride All
  Require all granted
</Directory>
CONF
a2ensite moodle
a2enmod rewrite
# Restart (not just start): apt's postinst may have auto-started Apache
# before the site config was enabled, leaving the alias unloaded.
systemctl enable apache2 >/dev/null 2>&1
systemctl restart apache2
systemctl enable --now mariadb
# Moodle requirements: max_input_vars>=5000 y límites razonables.
cat > /etc/php/8.3/apache2/conf.d/99-moodle.ini <<INI
memory_limit = 256M
max_input_vars = 5000
post_max_size = 100M
upload_max_filesize = 100M
INI
systemctl reload apache2
# Publish connection details inside the guest (bashrc + motd).
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Moodle
 URL        : http://$VMIP/moodle/
 Admin      : se crea en el asistente web inicial (/moodle/admin)
 Database   : mariadb | db=moodle | user=moodle | pass=$MOODLE_PASS
 IMPORTANTE : al terminar CAMBIA todas las contrasenas (admin y BD)
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() {
  [ -f "$1" ] || return 0
  grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"
}
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6)
[ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
	echo "Moodle provisioned."
`,

	// ---- vpn ----

	"wireguard-easy": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# Install Docker
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
# WireGuard module (built-in on kernel 5.6+, safety net)
modprobe wireguard 2>/dev/null || true
# IP forwarding
cat > /etc/sysctl.d/90-wireguard.conf <<'EOF'
net.ipv4.ip_forward=1
net.ipv4.conf.all.src_valid_mark=1
EOF
sysctl -p /etc/sysctl.d/90-wireguard.conf
WG_PASS="$(openssl rand -base64 12 | tr -d '\n' | cut -c1-16)"
mkdir -p /opt/wg-easy
cat > /opt/wg-easy/docker-compose.yml <<COMPOSE
services:
  wg-easy:
    image: ghcr.io/wg-easy/wg-easy:15
    container_name: wg-easy
    network_mode: host
    volumes:
      - /opt/wg-easy:/etc/wireguard
    cap_add:
      - NET_ADMIN
    environment:
      - INSECURE=true
      - INIT_ENABLED=true
      - INIT_USERNAME=admin
      - INIT_PASSWORD=${WG_PASS}
      - INIT_PORT=51820
    restart: unless-stopped
COMPOSE
cd /opt/wg-easy && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : WireGuard Easy
 URL        : http://$VMIP:51821
 VPN        : UDP 51820
 Admin      : admin / ${WG_PASS}
 IMPORTANTE : cambia la contrasena despues
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "WireGuard Easy provisioned."
`,

	"openvpn-ui": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
mkdir -p /dev/net && mknod /dev/net/tun c 10 200 2>/dev/null || true
OVPN_PASS="$(openssl rand -base64 12 | tr -d '\n' | cut -c1-16)"
mkdir -p /opt/openvpn-ui/{openvpn/conf,openvpn/db}
cat > /opt/openvpn-ui/docker-compose.yml <<COMPOSE
services:
  ovpn:
    image: shuricksumy/openvpn-ui:latest
    container_name: openvpn
    working_dir: /etc/openvpn/easy-rsa
    environment:
      - OPENVPN_ADMIN_USERNAME=admin
      - OPENVPN_ADMIN_PASSWORD=${OVPN_PASS}
      - SITE_NAME=VPN-Admin
      - APP_PORT=8080
    ports:
      - "8080:8080/tcp"
      - "1194:1194/udp"
    devices:
      - /dev/net/tun
    cap_add:
      - NET_ADMIN
    volumes:
      - ./openvpn/conf:/etc/openvpn
      - ./openvpn/db:/opt/openvpn-gui/db
    restart: always
COMPOSE
cd /opt/openvpn-ui && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : OpenVPN UI
 URL        : http://$VMIP:8080
 VPN        : UDP 1194
 Admin      : admin / ${OVPN_PASS}
 IMPORTANTE : completa el wizard en la web UI
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "OpenVPN UI provisioned."
`,

	// ---- devops ----

	"portainer-ce": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
docker volume create portainer_data 2>/dev/null || true
docker run -d --name portainer --restart=always \
  -p 9443:9443 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v portainer_data:/data \
  portainer/portainer-ce:lts
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Portainer CE
 URL        : https://$VMIP:9443
 Admin      : crea tu cuenta en la primera visita
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Portainer CE provisioned."
`,

	"gitea": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y git sqlite3
GITEA_VERSION="1.22.6"
adduser --system --shell /bin/bash --gecos 'Git Version Control' --group --disabled-password --home /home/git git 2>/dev/null || true
wget -q -O /usr/local/bin/gitea "https://dl.gitea.com/gitea/${GITEA_VERSION}/gitea-${GITEA_VERSION}-linux-amd64"
chmod +x /usr/local/bin/gitea
mkdir -p /var/lib/gitea/{custom,data,log} /etc/gitea
chown -R git:git /var/lib/gitea
chmod -R 750 /var/lib/gitea
chown root:git /etc/gitea
chmod 770 /etc/gitea
cat > /etc/gitea/app.ini <<'INI'
[database]
DB_TYPE = sqlite3
PATH = /var/lib/gitea/data/gitea.db
[repository]
ROOT = /var/lib/gitea/data/gitea-repositories
[server]
DOMAIN = localhost
HTTP_PORT = 3001
ROOT_URL = http://localhost:3001/
DISABLE_SSH = false
SSH_PORT = 22
[service]
DISABLE_REGISTRATION = false
REQUIRE_SIGNIN_VIEW = false
[security]
INSTALL_LOCK = true
[log]
MODE = file
LEVEL = Info
ROOT_PATH = /var/lib/gitea/log
[openid]
ENABLE_OPENID_SIGNIN = false
ENABLE_OPENID_SIGNUP = false
INI
chown root:git /etc/gitea/app.ini
chmod 640 /etc/gitea/app.ini
cat > /etc/systemd/system/gitea.service <<'UNIT'
[Unit]
Description=Gitea
After=network.target
[Service]
Type=simple
User=git
Group=git
WorkingDirectory=/var/lib/gitea/
ExecStart=/usr/local/bin/gitea web --config /etc/gitea/app.ini
Restart=always
Environment=GITEA_WORK_DIR=/var/lib/gitea/
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now gitea
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Gitea
 URL        : http://$VMIP:3001
 Admin      : crea tu cuenta en la primera visita
 SSH        : git clone ssh://git@$VMIP:22/...
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Gitea provisioned."
`,

	// ---- storage ----

	"vaultwarden": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
VW_TOKEN="$(openssl rand -base64 48)"
mkdir -p /opt/vaultwarden
docker run -d --name vaultwarden --restart unless-stopped \
  -v /opt/vaultwarden:/data \
  -e ADMIN_TOKEN="${VW_TOKEN}" \
  -e SIGNUPS_ALLOWED=true \
  -p 8080:80 \
  vaultwarden/server:latest
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Vaultwarden
 URL        : http://$VMIP:8080
 Admin Token : ${VW_TOKEN}
 IMPORTANTE : HTTPS necesario para web vault
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Vaultwarden provisioned."
`,

	"minio": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
MINIO_USER="minioadmin"
MINIO_PASS="$(openssl rand -base64 24 | tr -d '\n' | tr '/+' 'AB' | cut -c1-24)"
useradd --system --home /var/lib/minio --shell /sbin/nologin minio-user 2>/dev/null || true
wget -q -O /usr/local/bin/minio "https://dl.min.io/server/minio/release/linux-amd64/minio"
chmod +x /usr/local/bin/minio
mkdir -p /var/lib/minio/data
chown -R minio-user:minio-user /var/lib/minio
cat > /etc/default/minio <<EOF
MINIO_VOLUMES="/var/lib/minio/data"
MINIO_OPTS="--console-address :9001"
MINIO_ROOT_USER=${MINIO_USER}
MINIO_ROOT_PASSWORD=${MINIO_PASS}
EOF
chmod 600 /etc/default/minio
cat > /etc/systemd/system/minio.service <<'UNIT'
[Unit]
Description=MinIO
Wants=network-online.target
After=network-online.target
[Service]
User=minio-user
Group=minio-user
EnvironmentFile=-/etc/default/minio
ExecStart=/usr/local/bin/minio server $MINIO_OPTS $MINIO_VOLUMES
Restart=always
LimitNOFILE=65536
[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now minio
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : MinIO
 S3 API     : http://$VMIP:9000
 Console    : http://$VMIP:9001
 User       : ${MINIO_USER}
 Pass       : ${MINIO_PASS}
 IMPORTANTE : cambia las credenciales despues
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "MinIO provisioned."
`,

	// ---- web/monitoring ----

	"nginx-proxy-manager": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
mkdir -p /opt/npm/{data,letsencrypt}
cat > /opt/npm/docker-compose.yml <<'COMPOSE'
services:
  app:
    image: jc21/nginx-proxy-manager:latest
    restart: unless-stopped
    ports:
      - '80:80'
      - '443:443'
      - '81:81'
    environment:
      TZ: UTC
    volumes:
      - ./data:/data
      - ./letsencrypt:/etc/letsencrypt
COMPOSE
cd /opt/npm && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Nginx Proxy Manager
 Admin UI   : http://$VMIP:81
 Default    : admin@example.com / changeme
 IMPORTANTE : cambia las credenciales despues
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Nginx Proxy Manager provisioned."
`,

	"homer": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
mkdir -p /opt/homer/assets
cat > /opt/homer/docker-compose.yml <<'COMPOSE'
services:
  homer:
    image: b4bz/homer:latest
    container_name: homer
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - /opt/homer/assets:/www/assets
    environment:
      - INIT_ASSETS=1
COMPOSE
cd /opt/homer && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Homer Dashboard
 URL        : http://$VMIP:8080
 Config     : /opt/homer/assets/config.yml
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Homer provisioned."
`,

	"pihole": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
# Free port 53 from systemd-resolved
mkdir -p /etc/systemd/resolved.conf.d
cat > /etc/systemd/resolved.conf.d/no-stub.conf <<'EOF'
[Resolve]
DNSStubListener=no
EOF
rm -f /etc/resolv.conf
ln -s /run/systemd/resolve/resolv.conf /etc/resolv.conf
systemctl restart systemd-resolved
for i in $(seq 1 10); do ss -lntp | grep -q ':53 ' || break; sleep 2; done
PIHOLE_PASS="$(openssl rand -base64 12 | tr -d '\n' | cut -c1-16)"
mkdir -p /opt/pihole/etc-pihole
cat > /opt/pihole/docker-compose.yml <<COMPOSE
services:
  pihole:
    container_name: pihole
    image: pihole/pihole:latest
    ports:
      - "53:53/tcp"
      - "53:53/udp"
      - "80:80/tcp"
    environment:
      TZ: UTC
      FTLCONF_webserver_api_password: '${PIHOLE_PASS}'
      FTLCONF_dns_listeningMode: ALL
    volumes:
      - ./etc-pihole:/etc/pihole
    cap_add:
      - NET_ADMIN
    restart: unless-stopped
COMPOSE
cd /opt/pihole && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Pi-hole
 URL        : http://$VMIP/admin
 DNS        : $VMIP:53
 Password   : ${PIHOLE_PASS}
 IMPORTANTE : usa este DNS en tus dispositivos
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Pi-hole provisioned."
`,

	"adguard-home": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
# Free port 53
systemctl disable --now systemd-resolved 2>/dev/null || true
systemctl mask systemd-resolved 2>/dev/null || true
echo "nameserver 1.1.1.1" > /etc/resolv.conf
AGH_VERSION="v0.107.45"
wget -q -O /tmp/adguard.tar.gz "https://github.com/AdguardTeam/AdGuardHome/releases/download/${AGH_VERSION}/AdGuardHome_linux_amd64.tar.gz"
tar -xzf /tmp/adguard.tar.gz -C /opt/
rm /tmp/adguard.tar.gz
ADMIN_PASS="$(openssl rand -base64 12 | tr -d '\n' | cut -c1-16)"
apt-get update -y && apt-get install -y apache2-utils >/dev/null 2>&1
HASH="$(htpasswd -bnBC 10 "" "${ADMIN_PASS}" | cut -d: -f2)"
cat > /opt/AdGuardHome/AdGuardHome.yaml <<YAML
http:
  address: 0.0.0.0:80
  session_ttl: 720h
users:
  - name: admin
    password: "${HASH}"
auth_attempts: 5
block_auth_min: 15
theme: auto
dns:
  bind_hosts:
    - 0.0.0.0
  port: 53
  protection_enabled: true
  blocking_mode: default
  blocked_response_ttl: 10
  upstream_dns:
    - https://dns.google/dns-query
    - https://dns.cloudflare.com/dns-query
  bootstrap_dns:
    - 1.1.1.1
    - 8.8.8.8
  fallback_dns:
    - 8.8.4.4
  ratelimit: 50
  enabled: true
filters:
  - enabled: true
    url: https://adguardteam.github.io/HostlistsRegistry/assets/filter_1.txt
    name: AdGuard DNS filter
    id: 1
schema_version: 29
YAML
cd /opt/AdGuardHome && ./AdGuardHome -s install
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : AdGuard Home
 URL        : http://$VMIP
 DNS        : $VMIP:53
 Admin      : admin / ${ADMIN_PASS}
 IMPORTANTE : usa este DNS en tus dispositivos
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "AdGuard Home provisioned."
`,

	"beszel": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
mkdir -p /opt/beszel
cat > /opt/beszel/docker-compose.yml <<'COMPOSE'
services:
  beszel:
    image: henrygd/beszel:latest
    container_name: beszel
    restart: unless-stopped
    ports:
      - "8090:8090"
    volumes:
      - ./beszel_data:/beszel_data
  beszel-agent:
    image: henrygd/beszel-agent:latest
    container_name: beszel-agent
    restart: unless-stopped
    network_mode: host
    volumes:
      - ./beszel-agent_data:/beszel_data
      - /var/run/docker.sock:/var/run/docker.sock:ro
COMPOSE
cd /opt/beszel && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Beszel
 Hub        : http://$VMIP:8090
 Admin      : crea tu cuenta en la primera visita
 Agent      : beszel-agent en el mismo host
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Beszel provisioned."
`,

	"uptime-kuma": `#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v docker &>/dev/null; then
  apt-get update -y
  apt-get install -y ca-certificates curl gnupg
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
mkdir -p /opt/uptime-kuma
cat > /opt/uptime-kuma/docker-compose.yml <<'COMPOSE'
services:
  uptime-kuma:
    image: louislam/uptime-kuma:2
    container_name: uptime-kuma
    restart: unless-stopped
    ports:
      - "3001:3001"
    volumes:
      - ./uptime-kuma:/app/data
COMPOSE
cd /opt/uptime-kuma && docker compose up -d
VMIP=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src") print $(i+1)}')
[ -z "$VMIP" ] && VMIP=$(hostname -I | awk '{print $1}')
cat > /etc/webkvm-app.txt <<EOF
==========================================
 WebKVM App : Uptime Kuma
 URL        : http://$VMIP:3001
 Admin      : crea tu cuenta en la primera visita
 Log        : /var/log/webkvm-provision.log
==========================================
EOF
MARK='# >>> WEBKVM APP INFO >>>'
append_info() { [ -f "$1" ] || return 0; grep -qF "$MARK" "$1" || printf '\n%s\n[ -r /etc/webkvm-app.txt ] && cat /etc/webkvm-app.txt\n# <<< WEBKVM APP INFO <<<\n' "$MARK" >> "$1"; }
append_info /root/.bashrc
UHOME=$(getent passwd 1000 | cut -d: -f6); [ -n "$UHOME" ] && append_info "$UHOME/.bashrc"
echo "Uptime Kuma provisioned."
`,
}
