# Checklist de validación guiada (UI)

Para un operador que valida las funciones Cluster/HA de WebKVM desde la UI web
en un clúster real. Cada paso da la acción, el resultado esperado, una
comprobación de **solo lectura** en el nodo y qué enviar si falla. Contexto y
códigos de error: [HA-CLUSTER.es.md](HA-CLUSTER.es.md). Versión en inglés:
[GUIDED-VALIDATION.md](GUIDED-VALIDATION.md).

Estado de lo que pruebas: los flujos de la UI de abajo (SSH gestionado, consola
remota, edición remota, asistente de clúster, HA por carga, "Probar el
fencing") están implementados y con tests unitarios, pero **no probados en
vivo** antes de esta checklist. Cualquier fallo es información útil; no lo
esquives en silencio.

## Antes de empezar

- Hazlo en una ventana de mantenimiento, en un clúster de pruebas o con cargas
  desechables. Los pasos 8 y 9 cambian el clúster.
- Usa una VM de pruebas (KVM) y un contenedor desechable (Incus). Nunca empieces
  con cargas de producción.
- Todo lo de "Comprueba en el nodo" solo lee estado. Nada de eso cambia el
  clúster.
- Cuando algo falle, copia el **código de error exacto** que muestra la UI (por
  ejemplo `ssh_auth_denied`) y la salida del comando indicado.

Comandos de solo lectura usados abajo:

```bash
pcs status
pcs status xml | grep fence_event
pcs stonith history show NODO
incus list
incus cluster list
virsh -c qemu:///system list --all
sudo webkvm-cli ha deps check
```

## Checklist

### 1. Abre el panel por la VIP de la LAN

1. En el navegador abre `https://VIP-LAN:8080` (la dirección de TU red, no la
   red privada del clúster).
2. Esperado: carga la página de login (no en blanco, sin bucle de certificado
   más allá del aviso habitual de autofirmado).
3. Comprueba en el nodo: `pcs status` muestra `webkvm-vip-lan` (o tu recurso de
   VIP de la LAN) y `webkvm-app` Started en un nodo.
4. Si falla, envíame: la URL exacta que usaste, lo que muestra el navegador y
   la salida de `pcs status`.

### 2. Inicia sesión como administrador

1. Entra con una cuenta admin. Los nodos remotos y las acciones HA son solo
   para admin.
2. Esperado: se abre la página Nodos y muestra la sección del clúster.
3. Comprueba en el nodo: `pcs status` muestra quórum (`partition with quorum`).
4. Si falla, envíame: el mensaje mostrado y la salida de `pcs status`.

### 3. Fija la dirección de cada nodo en tu red (`ui_url`)

1. Página Nodos, edita cada nodo y rellena "Dirección en tu red (URL)", por
   ejemplo `https://192.0.2.10:8080`. Solo `esquema://host:puerto`.
2. Esperado: se guarda; el botón "Abrir WebKVM" de ese nodo abre su propia UI
   desde tu PC.
3. Comprueba en el nodo: `ip -br addr` (solo lectura) muestra esa dirección en
   el nodo.
4. Si falla, envíame: el texto de error bajo el campo (por ejemplo "La dirección no
   puede contener ruta") y la dirección que escribiste.

### 4. Acceso del clúster: clave, autorizar, fijar, probar

Tarjeta "Acceso del clúster (SSH gestionado)". Repite 4.2 a 4.4 en cada nodo KVM
remoto.

1. **Generar clave del clúster.** Esperado: clave creada, algoritmo ed25519 y
   una huella visible.
   Si falla, envíame: el código de error (`ssh_store_error`, `ssh_unavailable`).
2. **Autorizar.** Copia el comando y ejecútalo EN el nodo con el usuario SSH
   del nodo. Esperado: sin salida y sin error.
   Comprueba en el nodo: `grep restrict ~/.ssh/authorized_keys` muestra la
   línea `restrict,port-forwarding,permitopen="127.0.0.1:*" ssh-ed25519 ...`.
3. **Comprobar huella del host y luego fijarla.** Compara la huella con la que
   informa el nodo y fíjala. Esperado: estado "Fijada".
   Comprueba en el nodo: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` (el
   valor SHA256 coincide con la UI).
   Si difiere, PARA y envíame las dos huellas.
4. **Probar conexión.** Esperado: "La conexión con NODO funciona", login SSH OK,
   libvirt OK con su versión, "KVM disponible" y un número de VMs.
   Comprueba en el nodo: `virsh -c qemu:///system list --all` muestra el mismo
   número de VMs.
5. Si falla, envíame: el código y la pista mostrados. Típicos:
   `ssh_auth_denied` (clave sin autorizar; prueba otra vez tras autorizar),
   `host_key_changed`, `node_unreachable`, `libvirt_unavailable`,
   `transport_unsupported` (ver HA-CLUSTER.es.md, sección 11). Y la salida de
   `sudo journalctl -u sshd -n 30 --no-pager` en ese nodo.

### 5. Abre una VM remota y su consola

1. En la lista de VMs elige una que viva en otro nodo (la lista está federada;
   muestra el nodo). Ábrela.
2. Esperado: carga el detalle; la consola (noVNC) muestra la pantalla de la VM.
   SPICE en el navegador no se ofrece para VMs remotas: usa noVNC o el fichero
   `.vv`.
3. Comprueba en el nodo dueño de la VM: `virsh -c qemu:///system list` la
   muestra en marcha; `ss -ltn | grep 590` muestra un puerto VNC local en
   `127.0.0.1`.
4. Si falla, envíame: el código (`vm_not_running`, `console_forward_denied`,
   `console_busy`, `ticket_scope`, `remote_unsupported`) y, en el nodo, la
   salida de `virsh -c qemu:///system dumpxml VM | grep graphics`.

### 6. Edita el hardware de una VM remota

1. En una VM de pruebas de un nodo remoto cambia algo inocuo (vCPU o RAM) y
   guarda. Reinicia la energía si la UI lo pide.
2. Esperado: se guarda sin error; el valor nuevo aparece al recargar.
3. Comprueba en el nodo: `virsh -c qemu:///system dumpxml VM | grep -E "<vcpu|<memory"`
   muestra los valores nuevos.
4. Si falla, envíame: el código y el texto del error, y el mismo extracto de
   `dumpxml`.

### 7. Incus: abre una instancia de otro miembro

1. Abre una instancia Incus que corra en otro miembro del clúster.
2. Esperado: el detalle carga desde cualquier nodo.
3. Comprueba en un nodo: `incus list` e `incus cluster list` muestran la
   instancia en ese miembro y todos los miembros `ONLINE`.
4. Si falla, envíame: el código de error y la salida de `incus cluster list`.

### 8. Activa y desactiva HA en una carga de pruebas

Requisito previo: una prueba de fencing superada (paso 9). Sin ella, activar se
rechaza con `fencing_test_required`; ese rechazo es en sí un resultado válido de
este paso.

1. Elige una carga desechable. Incus: su perfil debe usar una red que exista en
   todos los miembros y `boot.autostart=false` (ver HA-CLUSTER.es.md, 8.3).
   KVM: la VM debe estar apagada y vivir en el volumen DRBD replicado.
2. Activa "Alta disponibilidad" y pulsa antes "Ver comandos pcs". Esperado: una
   lista de comandos, nada cambiado.
   Si está bloqueada, "¿Por qué?" lista motivos: envíame cada código de motivo
   mostrado.
3. Confirma "Activar HA". Esperado: "HA activado para NOMBRE"; el panel "Cargas
   HA" la lista como en ejecución.
   Comprueba en un nodo: `pcs status` muestra `ha-incus-PROYECTO-NOMBRE`
   (Incus) o `ha-kvm-NOMBRE-grp` (KVM) Started; `incus list` /
   `virsh -c qemu:///system list` muestra la instancia en marcha en el mismo
   nodo.
4. Usa "Mover" a otro nodo. Esperado: se reinicia allí en unos 10 a 15 s
   (recurso Incus medido).
   Comprueba: `pcs status` muestra el nodo nuevo; `incus list` coincide.
5. Desactiva HA con "Desactivar HA", modo "Detenerla". Esperado: "HA
   desactivado para NOMBRE"; los recursos desaparecen.
   Comprueba: `pcs status` ya no lista los recursos; `incus list` muestra la
   instancia parada.
6. Si falla, envíame: el código de error, los `blockers` o `reasons` mostrados y
   la salida de `pcs status`, `incus list`, `incus config show NOMBRE` (sin
   secretos).

### 9. Fencing: simula un fallo

Usa un nodo que NO ejecute el plano de control. Hazlo en una ventana.

1. Panel "Probar el fencing". Esperado: indica cómo aísla el clúster. Con SBD
   solo watchdog ofrece "Simular un fallo".
   NO uses el fencing directo sobre un nodo sano: se rechaza
   (`watchdog_only_unreliable`) porque no demuestra nada.
2. Elige el nodo y copia el comando mostrado. Ejecútalo tú EN ese nodo:

   ```bash
   sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'
   ```

3. Pulsa "Ya lo he ejecutado: esperar la evidencia". Esperado: el nodo
   desaparece unos 20 a 30 s y vuelve solo; la página termina con "Fencing
   demostrado: Pacemaker aisló NODO".
4. Comprueba en otro nodo: `pcs status` muestra 2 de 3 en línea mientras está
   caído y el quórum se mantiene; `pcs stonith history show NODO` lista el
   evento; `pcs status xml | grep fence_event` muestra `status="success"`.
5. Cuando el nodo vuelva: `pcs status` muestra todos los nodos Online, sin
   acciones fallidas; `incus cluster list` muestra todos los miembros `ONLINE`.
6. Si falla, envíame: la salida de `pcs status`, `pcs stonith history show
   NODO`, `pcs property config | grep -i stonith`, `ls /dev/watchdog*` y si el
   nodo llegó a reiniciarse de verdad.

### 10. Estado final

1. Esperado: `pcs status` sin acciones fallidas, quórum mantenido y
   `sudo webkvm-cli ha deps check` informa OK en cada nodo.
2. Comprueba: `incus cluster list` muestra todos los miembros `ONLINE`
   (ninguno `Evacuated`).
3. Si algo está `Evacuated`, envíame `incus cluster list` y
   `incus config get cluster.healing_threshold` (debe ser 0).

## Qué enviar

Por cada paso que falle, en un solo mensaje:

1. El número de paso y lo que hiciste.
2. El código de error exacto y el texto de la UI.
3. La salida de los comandos de solo lectura indicados en ese paso.
4. La hora (para cruzar los logs) y en qué nodo estabas.

No pegues contraseñas, tokens, claves privadas ni el contenido de
`DATA_DIR/cluster-ssh/`.
