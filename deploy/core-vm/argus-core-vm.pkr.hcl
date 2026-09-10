// Argus CORE appliance golden image (DESIGN §14d). Builds on top of the official Debian 13 (trixie)
// "generic" cloud qcow2 - the same base and build pattern as the probe golden image (argus-probe repo,
// deploy/probe-vm): the cloud image boots in seconds under plain TCG (GitHub runners have no KVM
// acceleration), a NoCloud seed CD gives Packer an SSH login, provisioning bakes the FULL core stack
// (Zabbix 7.0 server + frontend, PostgreSQL + TimescaleDB, Docker + the argus/argus-updater images),
// and the shutdown step strips machine identity and the build user so every deployed clone is unique
// and carries no shared credential.
//
// Nothing instance-specific is baked: no passwords, no database, no certificates. First boot serves a
// setup page (files/argus-core-firstboot.py) that collects the admin identity and then configures
// everything - see deploy/core-vm/README.md.
//
// "generic" (linux-image-amd64, full driver set), NOT "genericcloud" (virtio-only): the OVA delivery
// target has to boot on non-virtio hypervisors (VMware/VirtualBox SCSI/SATA) - same rationale as the
// probe image.

packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "~> 1.1"
    }
  }
}

variable "debian_image_url" {
  type        = string
  default     = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2"
  description = "Base Debian cloud image (qcow2) - the 'generic' (full-driver) variant, see the header note."
}

variable "debian_image_checksum" {
  type        = string
  default     = "file:https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS"
  description = "Checksum of the base image; 'file:<url>' pulls Debian's published SHA512SUMS."
}

variable "output_directory" {
  type    = string
  default = "output"
}

variable "vm_name" {
  type    = string
  default = "argus-core-vm.qcow2"
}

variable "disk_size" {
  type        = string
  default     = "100G"
  description = "Virtual disk size. Thin-provisioned (the shipped qcow2/VMDK stays small); the history/trends database grows into it over time."
}

source "qemu" "argus-core" {
  iso_url      = var.debian_image_url
  iso_checksum = var.debian_image_checksum
  disk_image   = true // the source is a bootable disk, not an install ISO
  disk_size    = var.disk_size
  format       = "qcow2"

  accelerator = "none" // TCG: works without nested KVM (GitHub runners); the cloud image still boots fast
  cpus        = 2
  memory      = 2048
  headless    = true

  // NoCloud seed CD so cloud-init creates the build-only 'packer' user for SSH.
  cd_label = "cidata"
  cd_files = ["./build-seed/user-data", "./build-seed/meta-data"]

  disk_interface = "virtio"
  net_device     = "virtio-net"

  ssh_username = "packer"
  ssh_password = "packer"
  ssh_timeout  = "15m" // first boot + cloud-init user creation + apt can take a while under TCG

  output_directory = var.output_directory
  vm_name          = var.vm_name
  disk_compression = true

  // Strip identity and the build user in a single SSH command, then power off. Doing the ssh-host-key
  // and machine-id removal here (not in provision.sh) keeps the live build session alive - a new SSH
  // connection would fail once the host keys are gone. (provision.sh already purged cloud-init and its
  // state, so there's no `cloud-init clean` to run here.) The first-boot service regenerates SSH host
  // keys; systemd regenerates the machine-id.
  shutdown_command = "sudo bash -c 'rm -f /etc/ssh/ssh_host_* /etc/machine-id /var/lib/dbus/machine-id; touch /etc/machine-id; userdel -f -r packer 2>/dev/null || true; shutdown -P now'"
  shutdown_timeout = "5m"
}

build {
  sources = ["source.qemu.argus-core"]

  // No trailing slash on source: uploads the directory itself, creating /tmp/files (the destination
  // dir need not pre-exist, unlike the "contents" form).
  provisioner "file" {
    source      = "files"
    destination = "/tmp"
  }

  // The manual-install script doubles as the image package installer (SETUP_MODE=image runs only its
  // repo/package/patching phases), so the appliance and the manual path install the exact same stack.
  provisioner "file" {
    source      = "../core/setup-core.sh"
    destination = "/tmp/setup-core.sh"
  }

  // The TLS + tuning lines the first-boot service appends to zabbix_server.conf once the PKI exists.
  provisioner "file" {
    source      = "../core/zabbix_server.conf.snippet"
    destination = "/tmp/zabbix_server.conf.snippet"
  }

  provisioner "shell" {
    execute_command = "sudo -E bash '{{ .Path }}'"
    script          = "scripts/provision.sh"
  }
}
