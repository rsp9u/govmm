/*
// Copyright contributors to the Virtual Machine Manager for Go project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
*/

// Package qemu provides methods and types for launching and managing QEMU
// instances.  Instances can be launched with the LaunchQemu function and
// managed thereafter via QMPStart and the QMP object that this function
// returns.  To manage a qemu instance after it has been launched you need
// to pass the -qmp option during launch requesting the qemu instance to create
// a QMP unix domain manageent socket, e.g.,
// -qmp unix:/tmp/qmp-socket,server,nowait.  For more information see the
// example below.
package qemu

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"context"
)

// Machine describes the machine type qemu will emulate.
type Machine struct {
	// Type is the machine type to be used by qemu.
	Type string

	// Acceleration are the machine acceleration options to be used by qemu.
	Acceleration string

	// Options are options for the machine type
	// For example gic-version=host and usb=off
	Options string
}

// Device is the qemu device interface.
type Device interface {
	Valid() bool
	QemuParams(config *Config) []string
}

// DeviceDriver is the device driver string.
type DeviceDriver string

const (
	// NVDIMM is the Non Volatile DIMM device driver.
	NVDIMM DeviceDriver = "nvdimm"

	// VirtioNetPCI is the virt-io pci networking device driver.
	VirtioNetPCI DeviceDriver = "virtio-net-pci"

	// VirtioNetCCW is the virt-io ccw networking device driver.
	VirtioNetCCW DeviceDriver = "virtio-net-ccw"

	// VirtioBlock is the block device driver.
	VirtioBlock DeviceDriver = "virtio-blk"

	// VirtioBlockPCI is a pci bus block device driver
	VirtioBlockPCI DeviceDriver = "virtio-blk-pci"

	// Console is the console device driver.
	Console DeviceDriver = "virtconsole"

	// VirtioSerialPort is the serial port device driver.
	VirtioSerialPort DeviceDriver = "virtserialport"

	// VirtioRng is the paravirtualized RNG device driver.
	VirtioRng DeviceDriver = "virtio-rng"

	// VirtioBalloon is the memory balloon device driver.
	VirtioBalloon DeviceDriver = "virtio-balloon"

	//VhostUserSCSI represents a SCSI vhostuser device type.
	VhostUserSCSI DeviceDriver = "vhost-user-scsi-pci"

	//VhostUserNet represents a net vhostuser device type.
	VhostUserNet DeviceDriver = "virtio-net-pci"

	//VhostUserBlk represents a block vhostuser device type.
	VhostUserBlk DeviceDriver = "vhost-user-blk-pci"

	//VhostUserFS represents a virtio-fs vhostuser device type
	VhostUserFS DeviceDriver = "vhost-user-fs-pci"

	// PCIBridgeDriver represents a PCI bridge device type.
	PCIBridgeDriver DeviceDriver = "pci-bridge"

	// PCIePCIBridgeDriver represents a PCIe to PCI bridge device type.
	PCIePCIBridgeDriver DeviceDriver = "pcie-pci-bridge"

	// VirtioBlockCCW is the CCW block device driver
	VirtioBlockCCW DeviceDriver = "virtio-blk-ccw"
)

// disableModern returns the parameters with the disable-modern option.
// In case the device driver is not a PCI device and it doesn't have the option
// an empty string is returned.
func (driver DeviceDriver) disableModern(disable bool) string {
	if !isVirtioPCI[driver] {
		return ""
	}

	if disable {
		return "disable-modern=true"
	}

	return "disable-modern=false"
}

// ObjectType is a string representing a qemu object type.
type ObjectType string

const (
	// MemoryBackendFile represents a guest memory mapped file.
	MemoryBackendFile ObjectType = "memory-backend-file"
)

// Object is a qemu object representation.
type Object struct {
	// Driver is the qemu device driver
	Driver DeviceDriver

	// Type is the qemu object type.
	Type ObjectType

	// ID is the user defined object ID.
	ID string

	// DeviceID is the user defined device ID.
	DeviceID string

	// MemPath is the object's memory path.
	// This is only relevant for memory objects
	MemPath string

	// Size is the object size in bytes
	Size uint64
}

// Valid returns true if the Object structure is valid and complete.
func (object Object) Valid() bool {
	switch object.Type {
	case MemoryBackendFile:
		if object.ID == "" || object.MemPath == "" || object.Size == 0 {
			return false
		}

	default:
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this Object device.
func (object Object) QemuParams(config *Config) []string {
	var objectParams []string
	var deviceParams []string
	var qemuParams []string

	deviceParams = append(deviceParams, string(object.Driver))
	deviceParams = append(deviceParams, fmt.Sprintf(",id=%s", object.DeviceID))

	switch object.Type {
	case MemoryBackendFile:
		objectParams = append(objectParams, string(object.Type))
		objectParams = append(objectParams, fmt.Sprintf(",id=%s", object.ID))
		objectParams = append(objectParams, fmt.Sprintf(",mem-path=%s", object.MemPath))
		objectParams = append(objectParams, fmt.Sprintf(",size=%d", object.Size))

		deviceParams = append(deviceParams, fmt.Sprintf(",memdev=%s", object.ID))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	qemuParams = append(qemuParams, "-object")
	qemuParams = append(qemuParams, strings.Join(objectParams, ""))

	return qemuParams
}

// FSDriver represents a qemu filesystem driver.
type FSDriver string

// SecurityModelType is a qemu filesystem security model type.
type SecurityModelType string

const (
	// Local is the local qemu filesystem driver.
	Local FSDriver = "local"

	// Handle is the handle qemu filesystem driver.
	Handle FSDriver = "handle"

	// Proxy is the proxy qemu filesystem driver.
	Proxy FSDriver = "proxy"
)

const (
	// None is like passthrough without failure reports.
	None SecurityModelType = "none"

	// PassThrough uses the same credentials on both the host and guest.
	PassThrough SecurityModelType = "passthrough"

	// MappedXattr stores some files attributes as extended attributes.
	MappedXattr SecurityModelType = "mapped-xattr"

	// MappedFile stores some files attributes in the .virtfs directory.
	MappedFile SecurityModelType = "mapped-file"
)

// FSDevice represents a qemu filesystem configuration.
type FSDevice struct {
	// Driver is the qemu device driver
	Driver DeviceDriver

	// FSDriver is the filesystem driver backend.
	FSDriver FSDriver

	// ID is the filesystem identifier.
	ID string

	// Path is the host root path for this filesystem.
	Path string

	// MountTag is the device filesystem mount point tag.
	MountTag string

	// SecurityModel is the security model for this filesystem device.
	SecurityModel SecurityModelType

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the FSDevice structure is valid and complete.
func (fsdev FSDevice) Valid() bool {
	if fsdev.ID == "" || fsdev.Path == "" || fsdev.MountTag == "" {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this filesystem device.
func (fsdev FSDevice) QemuParams(config *Config) []string {
	var fsParams []string
	var deviceParams []string
	var qemuParams []string

	deviceParams = append(deviceParams, string(fsdev.Driver))
	if s := fsdev.Driver.disableModern(fsdev.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}
	deviceParams = append(deviceParams, fmt.Sprintf(",fsdev=%s", fsdev.ID))
	deviceParams = append(deviceParams, fmt.Sprintf(",mount_tag=%s", fsdev.MountTag))
	if isVirtioPCI[fsdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", fsdev.ROMFile))
	}
	if isVirtioCCW[fsdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", fsdev.DevNo))
	}

	fsParams = append(fsParams, string(fsdev.FSDriver))
	fsParams = append(fsParams, fmt.Sprintf(",id=%s", fsdev.ID))
	fsParams = append(fsParams, fmt.Sprintf(",path=%s", fsdev.Path))
	fsParams = append(fsParams, fmt.Sprintf(",security_model=%s", fsdev.SecurityModel))

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	qemuParams = append(qemuParams, "-fsdev")
	qemuParams = append(qemuParams, strings.Join(fsParams, ""))

	return qemuParams
}

// CharDeviceBackend is the character device backend for qemu
type CharDeviceBackend string

const (
	// Pipe creates a 2 way connection to the guest.
	Pipe CharDeviceBackend = "pipe"

	// Socket creates a 2 way stream socket (TCP or Unix).
	Socket CharDeviceBackend = "socket"

	// CharConsole sends traffic from the guest to QEMU's standard output.
	CharConsole CharDeviceBackend = "console"

	// Serial sends traffic from the guest to a serial device on the host.
	Serial CharDeviceBackend = "serial"

	// TTY is an alias for Serial.
	TTY CharDeviceBackend = "tty"

	// PTY creates a new pseudo-terminal on the host and connect to it.
	PTY CharDeviceBackend = "pty"
)

// CharDevice represents a qemu character device.
type CharDevice struct {
	Backend CharDeviceBackend

	// Driver is the qemu device driver
	Driver DeviceDriver

	// Bus is the serial bus associated to this device.
	Bus string

	// DeviceID is the user defined device ID.
	DeviceID string

	ID   string
	Path string
	Name string

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the CharDevice structure is valid and complete.
func (cdev CharDevice) Valid() bool {
	if cdev.ID == "" || cdev.Path == "" {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this character device.
func (cdev CharDevice) QemuParams(config *Config) []string {
	var cdevParams []string
	var deviceParams []string
	var qemuParams []string

	deviceParams = append(deviceParams, string(cdev.Driver))
	if s := cdev.Driver.disableModern(cdev.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}
	if cdev.Bus != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",bus=%s", cdev.Bus))
	}
	deviceParams = append(deviceParams, fmt.Sprintf(",chardev=%s", cdev.ID))
	deviceParams = append(deviceParams, fmt.Sprintf(",id=%s", cdev.DeviceID))
	if cdev.Name != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",name=%s", cdev.Name))
	}
	if isVirtioPCI[cdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", cdev.ROMFile))
	}

	if isVirtioCCW[cdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", cdev.DevNo))
	}

	cdevParams = append(cdevParams, string(cdev.Backend))
	cdevParams = append(cdevParams, fmt.Sprintf(",id=%s", cdev.ID))
	if cdev.Backend == Socket {
		cdevParams = append(cdevParams, fmt.Sprintf(",path=%s,server,nowait", cdev.Path))
	} else {
		cdevParams = append(cdevParams, fmt.Sprintf(",path=%s", cdev.Path))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	qemuParams = append(qemuParams, "-chardev")
	qemuParams = append(qemuParams, strings.Join(cdevParams, ""))

	return qemuParams
}

// NetDeviceType is a qemu networking device type.
type NetDeviceType string

const (
	// TAP is a TAP networking device type.
	TAP NetDeviceType = "tap"

	// MACVTAP is a macvtap networking device type.
	MACVTAP NetDeviceType = "macvtap"

	// IPVTAP is a ipvtap virtual networking device type.
	IPVTAP NetDeviceType = "ipvtap"

	// VETHTAP is a veth-tap virtual networking device type.
	VETHTAP NetDeviceType = "vethtap"

	// VFIO is a direct assigned PCI device or PCI VF
	VFIO NetDeviceType = "VFIO"

	// VHOSTUSER is a vhost-user port (socket)
	VHOSTUSER NetDeviceType = "vhostuser"
)

// NetDevice represents a guest networking device
type NetDevice struct {
	// Type is the netdev type (e.g. tap).
	Type NetDeviceType

	// Driver is the qemu device driver
	Driver DeviceDriver

	// ID is the netdevice identifier.
	ID string

	// IfName is the interface name,
	IFName string

	// Bus is the bus path name of a PCI device.
	Bus string

	// Addr is the address offset of a PCI device.
	Addr string

	// DownScript is the tap interface deconfiguration script.
	DownScript string

	// Script is the tap interface configuration script.
	Script string

	// FDs represents the list of already existing file descriptors to be used.
	// This is mostly useful for mq support.
	FDs      []*os.File
	VhostFDs []*os.File

	// VHost enables virtio device emulation from the host kernel instead of from qemu.
	VHost bool

	// MACAddress is the networking device interface MAC address.
	MACAddress string

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the NetDevice structure is valid and complete.
func (netdev NetDevice) Valid() bool {
	if netdev.ID == "" || netdev.IFName == "" {
		return false
	}

	switch netdev.Type {
	case TAP:
		return true
	case MACVTAP:
		return true
	default:
		return false
	}
}

// mqParameter returns the parameters for multi-queue driver. If the driver is a PCI device then the
// vector flag is required. If the driver is a CCW type than the vector flag is not implemented and only
// multi-queue option mq needs to be activated. See comment in libvirt code at
// https://github.com/libvirt/libvirt/blob/6e7e965dcd3d885739129b1454ce19e819b54c25/src/qemu/qemu_command.c#L3633
func (netdev NetDevice) mqParameter() string {
	p := []string{",mq=on"}

	if isVirtioPCI[netdev.Driver] {
		// https://www.linux-kvm.org/page/Multiqueue
		// -netdev tap,vhost=on,queues=N
		// enable mq and specify msix vectors in qemu cmdline
		// (2N+2 vectors, N for tx queues, N for rx queues, 1 for config, and one for possible control vq)
		// -device virtio-net-pci,mq=on,vectors=2N+2...
		// enable mq in guest by 'ethtool -L eth0 combined $queue_num'
		// Clearlinux automatically sets up the queues properly
		// The agent implementation should do this to ensure that it is
		// always set
		vectors := len(netdev.FDs)*2 + 2
		p = append(p, fmt.Sprintf(",vectors=%d", vectors))
	}

	return strings.Join(p, "")
}

// QemuDeviceParams returns the -device parameters for this network device
func (netdev NetDevice) QemuDeviceParams(config *Config) []string {
	var deviceParams []string

	if netdev.Type.QemuDeviceParam() == "" {
		return nil
	}

	deviceParams = append(deviceParams, fmt.Sprintf("driver=%s", netdev.Type.QemuDeviceParam()))
	deviceParams = append(deviceParams, fmt.Sprintf(",netdev=%s", netdev.ID))
	deviceParams = append(deviceParams, fmt.Sprintf(",mac=%s", netdev.MACAddress))

	if netdev.Bus != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",bus=%s", netdev.Bus))
	}

	if netdev.Addr != "" {
		addr, err := strconv.Atoi(netdev.Addr)
		if err == nil && addr >= 0 {
			deviceParams = append(deviceParams, fmt.Sprintf(",addr=%x", addr))
		}
	}
	if s := netdev.Driver.disableModern(netdev.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}

	if len(netdev.FDs) > 0 {
		// Note: We are appending to the device params here
		deviceParams = append(deviceParams, netdev.mqParameter())
	}

	if isVirtioPCI[netdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", netdev.ROMFile))
	}

	if isVirtioCCW[netdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", netdev.DevNo))
	}

	return deviceParams
}

// QemuNetdevParams returns the -netdev parameters for this network device
func (netdev NetDevice) QemuNetdevParams(config *Config) []string {
	var netdevParams []string

	if netdev.Type.QemuNetdevParam() == "" {
		return nil
	}

	netdevParams = append(netdevParams, netdev.Type.QemuNetdevParam())
	netdevParams = append(netdevParams, fmt.Sprintf(",id=%s", netdev.ID))

	if netdev.VHost {
		netdevParams = append(netdevParams, ",vhost=on")
		if len(netdev.VhostFDs) > 0 {
			var fdParams []string
			qemuFDs := config.appendFDs(netdev.VhostFDs)
			for _, fd := range qemuFDs {
				fdParams = append(fdParams, fmt.Sprintf("%d", fd))
			}
			netdevParams = append(netdevParams, fmt.Sprintf(",vhostfds=%s", strings.Join(fdParams, ":")))
		}
	}

	if len(netdev.FDs) > 0 {
		var fdParams []string

		qemuFDs := config.appendFDs(netdev.FDs)
		for _, fd := range qemuFDs {
			fdParams = append(fdParams, fmt.Sprintf("%d", fd))
		}

		netdevParams = append(netdevParams, fmt.Sprintf(",fds=%s", strings.Join(fdParams, ":")))

	} else {
		netdevParams = append(netdevParams, fmt.Sprintf(",ifname=%s", netdev.IFName))
		if netdev.DownScript != "" {
			netdevParams = append(netdevParams, fmt.Sprintf(",downscript=%s", netdev.DownScript))
		}
		if netdev.Script != "" {
			netdevParams = append(netdevParams, fmt.Sprintf(",script=%s", netdev.Script))
		}
	}
	return netdevParams
}

// QemuParams returns the qemu parameters built out of this network device.
func (netdev NetDevice) QemuParams(config *Config) []string {
	var netdevParams []string
	var deviceParams []string
	var qemuParams []string

	// Macvtap can only be connected via fds
	if (netdev.Type == MACVTAP) && (len(netdev.FDs) == 0) {
		return nil // implicit error
	}

	if netdev.Type.QemuNetdevParam() != "" {
		netdevParams = netdev.QemuNetdevParams(config)
		if netdevParams != nil {
			qemuParams = append(qemuParams, "-netdev")
			qemuParams = append(qemuParams, strings.Join(netdevParams, ""))
		}
	}

	if netdev.Type.QemuDeviceParam() != "" {
		deviceParams = netdev.QemuDeviceParams(config)
		if deviceParams != nil {
			qemuParams = append(qemuParams, "-device")
			qemuParams = append(qemuParams, strings.Join(deviceParams, ""))
		}
	}

	return qemuParams
}

// SerialDevice represents a qemu serial device.
type SerialDevice struct {
	// Driver is the qemu device driver
	Driver DeviceDriver

	// ID is the serial device identifier.
	ID string

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the SerialDevice structure is valid and complete.
func (dev SerialDevice) Valid() bool {
	if dev.Driver == "" || dev.ID == "" {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this serial device.
func (dev SerialDevice) QemuParams(config *Config) []string {
	var deviceParams []string
	var qemuParams []string

	deviceParams = append(deviceParams, string(dev.Driver))
	if s := dev.Driver.disableModern(dev.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}
	deviceParams = append(deviceParams, fmt.Sprintf(",id=%s", dev.ID))
	if isVirtioPCI[dev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", dev.ROMFile))
	}

	if isVirtioCCW[dev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", dev.DevNo))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	return qemuParams
}

// BlockDeviceInterface defines the type of interface the device is connected to.
type BlockDeviceInterface string

// BlockDeviceAIO defines the type of asynchronous I/O the block device should use.
type BlockDeviceAIO string

// BlockDeviceFormat defines the image format used on a block device.
type BlockDeviceFormat string

const (
	// NoInterface for block devices with no interfaces.
	NoInterface BlockDeviceInterface = "none"

	// SCSI represents a SCSI block device interface.
	SCSI BlockDeviceInterface = "scsi"
)

const (
	// Threads is the pthread asynchronous I/O implementation.
	Threads BlockDeviceAIO = "threads"

	// Native is the pthread asynchronous I/O implementation.
	Native BlockDeviceAIO = "native"
)

const (
	// QCOW2 is the Qemu Copy On Write v2 image format.
	QCOW2 BlockDeviceFormat = "qcow2"
)

// BlockDevice represents a qemu block device.
type BlockDevice struct {
	Driver    DeviceDriver
	ID        string
	File      string
	Interface BlockDeviceInterface
	AIO       BlockDeviceAIO
	Format    BlockDeviceFormat
	SCSI      bool
	WCE       bool

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string

	// ShareRW enables multiple qemu instances to share the File
	ShareRW bool
}

// Valid returns true if the BlockDevice structure is valid and complete.
func (blkdev BlockDevice) Valid() bool {
	if blkdev.Driver == "" || blkdev.ID == "" || blkdev.File == "" {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this block device.
func (blkdev BlockDevice) QemuParams(config *Config) []string {
	var blkParams []string
	var deviceParams []string
	var qemuParams []string

	deviceParams = append(deviceParams, string(blkdev.Driver))
	if s := blkdev.Driver.disableModern(blkdev.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}
	deviceParams = append(deviceParams, fmt.Sprintf(",drive=%s", blkdev.ID))
	if !blkdev.SCSI {
		deviceParams = append(deviceParams, ",scsi=off")
	}

	if !blkdev.WCE {
		deviceParams = append(deviceParams, ",config-wce=off")
	}

	if isVirtioPCI[blkdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", blkdev.ROMFile))
	}

	if isVirtioCCW[blkdev.Driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", blkdev.DevNo))
	}

	if blkdev.ShareRW {
		deviceParams = append(deviceParams, fmt.Sprintf(",share-rw=on"))
	}

	blkParams = append(blkParams, fmt.Sprintf("id=%s", blkdev.ID))
	blkParams = append(blkParams, fmt.Sprintf(",file=%s", blkdev.File))
	blkParams = append(blkParams, fmt.Sprintf(",aio=%s", blkdev.AIO))
	blkParams = append(blkParams, fmt.Sprintf(",format=%s", blkdev.Format))
	blkParams = append(blkParams, fmt.Sprintf(",if=%s", blkdev.Interface))

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	qemuParams = append(qemuParams, "-drive")
	qemuParams = append(qemuParams, strings.Join(blkParams, ""))

	return qemuParams
}

// VhostUserDevice represents a qemu vhost-user device meant to be passed
// in to the guest
type VhostUserDevice struct {
	SocketPath     string //path to vhostuser socket on host
	CharDevID      string
	TypeDevID      string //variable QEMU parameter based on value of VhostUserType
	Address        string //used for MAC address in net case
	Tag            string //virtio-fs volume id for mounting inside guest
	CacheSize      uint32 //virtio-fs DAX cache size in MiB
	SharedVersions bool   //enable virtio-fs shared version metadata
	VhostUserType  DeviceDriver

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string
}

// Valid returns true if there is a valid structure defined for VhostUserDevice
func (vhostuserDev VhostUserDevice) Valid() bool {

	if vhostuserDev.SocketPath == "" || vhostuserDev.CharDevID == "" {
		return false
	}

	switch vhostuserDev.VhostUserType {
	case VhostUserNet:
		if vhostuserDev.TypeDevID == "" || vhostuserDev.Address == "" {
			return false
		}
	case VhostUserSCSI:
		if vhostuserDev.TypeDevID == "" {
			return false
		}
	case VhostUserBlk:
	case VhostUserFS:
		if vhostuserDev.Tag == "" {
			return false
		}
	default:
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this vhostuser device.
func (vhostuserDev VhostUserDevice) QemuParams(config *Config) []string {
	var qemuParams []string
	var charParams []string
	var netParams []string
	var devParams []string
	var driver DeviceDriver

	charParams = append(charParams, "socket")
	charParams = append(charParams, fmt.Sprintf("id=%s", vhostuserDev.CharDevID))
	charParams = append(charParams, fmt.Sprintf("path=%s", vhostuserDev.SocketPath))

	switch vhostuserDev.VhostUserType {
	// if network based vhost device:
	case VhostUserNet:
		driver = VhostUserNet
		netParams = append(netParams, "type=vhost-user")
		netParams = append(netParams, fmt.Sprintf("id=%s", vhostuserDev.TypeDevID))
		netParams = append(netParams, fmt.Sprintf("chardev=%s", vhostuserDev.CharDevID))
		netParams = append(netParams, "vhostforce")

		devParams = append(devParams, string(driver))
		devParams = append(devParams, fmt.Sprintf("netdev=%s", vhostuserDev.TypeDevID))
		devParams = append(devParams, fmt.Sprintf("mac=%s", vhostuserDev.Address))
	case VhostUserSCSI:
		driver = VhostUserSCSI
		devParams = append(devParams, string(driver))
		devParams = append(devParams, fmt.Sprintf("id=%s", vhostuserDev.TypeDevID))
		devParams = append(devParams, fmt.Sprintf("chardev=%s", vhostuserDev.CharDevID))
	case VhostUserBlk:
		driver = VhostUserBlk
		devParams = append(devParams, string(driver))
		devParams = append(devParams, "logical_block_size=4096")
		devParams = append(devParams, "size=512M")
		devParams = append(devParams, fmt.Sprintf("chardev=%s", vhostuserDev.CharDevID))
	case VhostUserFS:
		driver = VhostUserFS
		devParams = append(devParams, string(driver))
		devParams = append(devParams, fmt.Sprintf("chardev=%s", vhostuserDev.CharDevID))
		devParams = append(devParams, fmt.Sprintf("tag=%s", vhostuserDev.Tag))
		devParams = append(devParams, fmt.Sprintf("cache-size=%dM", vhostuserDev.CacheSize))
		if vhostuserDev.SharedVersions {
			devParams = append(devParams, "versiontable=/dev/shm/fuse_shared_versions")
		}
	default:
		return nil
	}

	if isVirtioPCI[driver] {
		devParams = append(devParams, fmt.Sprintf("romfile=%s", vhostuserDev.ROMFile))
	}

	qemuParams = append(qemuParams, "-chardev")
	qemuParams = append(qemuParams, strings.Join(charParams, ","))

	// if network based vhost device:
	if vhostuserDev.VhostUserType == VhostUserNet {
		qemuParams = append(qemuParams, "-netdev")
		qemuParams = append(qemuParams, strings.Join(netParams, ","))
	}
	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(devParams, ","))

	return qemuParams
}

// VFIODevice represents a qemu vfio device meant for direct access by guest OS.
type VFIODevice struct {
	// Bus-Device-Function of device
	BDF string

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string

	// VendorID specifies vendor id
	VendorID string

	// DeviceID specifies device id
	DeviceID string
}

// Valid returns true if the VFIODevice structure is valid and complete.
func (vfioDev VFIODevice) Valid() bool {
	return vfioDev.BDF != ""
}

// QemuParams returns the qemu parameters built out of this vfio device.
func (vfioDev VFIODevice) QemuParams(config *Config) []string {
	var qemuParams []string
	var deviceParams []string

	driver := Vfio

	deviceParams = append(deviceParams, fmt.Sprintf("%s,host=%s", driver, vfioDev.BDF))
	if isVirtioPCI[driver] {
		if vfioDev.VendorID != "" {
			deviceParams = append(deviceParams, fmt.Sprintf(",x-pci-vendor-id=%s", vfioDev.VendorID))
		}
		if vfioDev.DeviceID != "" {
			deviceParams = append(deviceParams, fmt.Sprintf(",x-pci-device-id=%s", vfioDev.DeviceID))
		}
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", vfioDev.ROMFile))
	}

	if isVirtioCCW[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", vfioDev.DevNo))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	return qemuParams
}

// SCSIController represents a SCSI controller device.
type SCSIController struct {
	ID string

	// Bus on which the SCSI controller is attached, this is optional
	Bus string

	// Addr is the PCI address offset, this is optional
	Addr string

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// IOThread is the IO thread on which IO will be handled
	IOThread string

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the SCSIController structure is valid and complete.
func (scsiCon SCSIController) Valid() bool {
	return scsiCon.ID != ""
}

// QemuParams returns the qemu parameters built out of this SCSIController device.
func (scsiCon SCSIController) QemuParams(config *Config) []string {
	var qemuParams []string
	var devParams []string

	driver := VirtioScsi
	devParams = append(devParams, fmt.Sprintf("%s,id=%s", driver, scsiCon.ID))
	if scsiCon.Bus != "" {
		devParams = append(devParams, fmt.Sprintf("bus=%s", scsiCon.Bus))
	}
	if scsiCon.Addr != "" {
		devParams = append(devParams, fmt.Sprintf("addr=%s", scsiCon.Addr))
	}
	if s := driver.disableModern(scsiCon.DisableModern); s != "" {
		devParams = append(devParams, s)
	}
	if scsiCon.IOThread != "" {
		devParams = append(devParams, fmt.Sprintf("iothread=%s", scsiCon.IOThread))
	}
	if isVirtioPCI[driver] {
		devParams = append(devParams, fmt.Sprintf("romfile=%s", scsiCon.ROMFile))
	}

	if isVirtioCCW[driver] {
		devParams = append(devParams, fmt.Sprintf("devno=%s", scsiCon.DevNo))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(devParams, ","))

	return qemuParams
}

// BridgeType is the type of the bridge
type BridgeType uint

const (
	// PCIBridge is a pci bridge
	PCIBridge BridgeType = iota

	// PCIEBridge is a pcie bridge
	PCIEBridge
)

// BridgeDevice represents a qemu bridge device like pci-bridge, pxb, etc.
type BridgeDevice struct {
	// Type of the bridge
	Type BridgeType

	// Bus number where the bridge is plugged, typically pci.0 or pcie.0
	Bus string

	// ID is used to identify the bridge in qemu
	ID string

	// Chassis number
	Chassis int

	// SHPC is used to enable or disable the standard hot plug controller
	SHPC bool

	// PCI Slot
	Addr string

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string
}

// Valid returns true if the BridgeDevice structure is valid and complete.
func (bridgeDev BridgeDevice) Valid() bool {
	if bridgeDev.Type != PCIBridge && bridgeDev.Type != PCIEBridge {
		return false
	}

	if bridgeDev.Bus == "" {
		return false
	}

	if bridgeDev.ID == "" {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of this bridge device.
func (bridgeDev BridgeDevice) QemuParams(config *Config) []string {
	var qemuParams []string
	var deviceParam []string
	var driver DeviceDriver

	switch bridgeDev.Type {
	case PCIEBridge:
		driver = PCIePCIBridgeDriver
		deviceParam = append(deviceParam, fmt.Sprintf("%s,bus=%s,id=%s", driver, bridgeDev.Bus, bridgeDev.ID))
	default:
		driver = PCIBridgeDriver
		shpc := "off"
		if bridgeDev.SHPC {
			shpc = "on"
		}
		deviceParam = append(deviceParam, fmt.Sprintf("%s,bus=%s,id=%s,chassis_nr=%d,shpc=%s", driver, bridgeDev.Bus, bridgeDev.ID, bridgeDev.Chassis, shpc))
	}

	if bridgeDev.Addr != "" {
		addr, err := strconv.Atoi(bridgeDev.Addr)
		if err == nil && addr >= 0 {
			deviceParam = append(deviceParam, fmt.Sprintf(",addr=%x", addr))
		}
	}

	if isVirtioPCI[driver] {
		deviceParam = append(deviceParam, fmt.Sprintf(",romfile=%s", bridgeDev.ROMFile))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParam, ""))

	return qemuParams
}

// VSOCKDevice represents a AF_VSOCK socket.
type VSOCKDevice struct {
	ID string

	ContextID uint64

	// VHostFD vhost file descriptor that holds the ContextID
	VHostFD *os.File

	// DisableModern prevents qemu from relying on fast MMIO.
	DisableModern bool

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

const (
	// MinimalGuestCID is the smallest valid context ID for a guest.
	MinimalGuestCID uint64 = 3

	// MaxGuestCID is the largest valid context ID for a guest.
	MaxGuestCID uint64 = 1<<32 - 1
)

const (
	// VSOCKGuestCID is the VSOCK guest CID parameter.
	VSOCKGuestCID = "guest-cid"
)

// Valid returns true if the VSOCKDevice structure is valid and complete.
func (vsock VSOCKDevice) Valid() bool {
	if vsock.ID == "" || vsock.ContextID < MinimalGuestCID || vsock.ContextID > MaxGuestCID {
		return false
	}

	return true
}

// QemuParams returns the qemu parameters built out of the VSOCK device.
func (vsock VSOCKDevice) QemuParams(config *Config) []string {
	var deviceParams []string
	var qemuParams []string

	driver := VHostVSock
	deviceParams = append(deviceParams, string(driver))
	if s := driver.disableModern(vsock.DisableModern); s != "" {
		deviceParams = append(deviceParams, fmt.Sprintf(",%s", s))
	}
	if vsock.VHostFD != nil {
		qemuFDs := config.appendFDs([]*os.File{vsock.VHostFD})
		deviceParams = append(deviceParams, fmt.Sprintf(",vhostfd=%d", qemuFDs[0]))
	}
	deviceParams = append(deviceParams, fmt.Sprintf(",id=%s", vsock.ID))
	deviceParams = append(deviceParams, fmt.Sprintf(",%s=%d", VSOCKGuestCID, vsock.ContextID))

	if isVirtioPCI[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",romfile=%s", vsock.ROMFile))
	}

	if isVirtioCCW[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf(",devno=%s", vsock.DevNo))
	}

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ""))

	return qemuParams
}

// RngDevice represents a random number generator device.
type RngDevice struct {
	// ID is the device ID
	ID string
	// Filename is entropy source on the host
	Filename string
	// MaxBytes is the bytes allowed to guest to get from the host’s entropy per period
	MaxBytes uint
	// Period is duration of a read period in seconds
	Period uint
	// ROMFile specifies the ROM file being used for this device.
	ROMFile string
	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// Valid returns true if the RngDevice structure is valid and complete.
func (v RngDevice) Valid() bool {
	return v.ID != ""
}

// QemuParams returns the qemu parameters built out of the RngDevice.
func (v RngDevice) QemuParams(_ *Config) []string {
	var qemuParams []string

	//-object rng-random,filename=/dev/hwrng,id=rng0
	var objectParams []string
	//-device virtio-rng-pci,rng=rng0,max-bytes=1024,period=1000
	var deviceParams []string

	driver := VirtioRng
	objectParams = append(objectParams, "rng-random")
	objectParams = append(objectParams, "id="+v.ID)

	deviceParams = append(deviceParams, string(driver))
	deviceParams = append(deviceParams, "rng="+v.ID)

	if isVirtioPCI[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf("romfile=%s", v.ROMFile))
	}

	if isVirtioCCW[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf("devno=%s", v.DevNo))
	}

	if v.Filename != "" {
		objectParams = append(objectParams, "filename="+v.Filename)
	}

	if v.MaxBytes > 0 {
		deviceParams = append(deviceParams, fmt.Sprintf("max-bytes=%d", v.MaxBytes))
	}

	if v.Period > 0 {
		deviceParams = append(deviceParams, fmt.Sprintf("period=%d", v.Period))
	}

	qemuParams = append(qemuParams, "-object")
	qemuParams = append(qemuParams, strings.Join(objectParams, ","))

	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ","))

	return qemuParams
}

// BalloonDevice represents a memory balloon device.
type BalloonDevice struct {
	DeflateOnOOM  bool
	DisableModern bool
	ID            string

	// ROMFile specifies the ROM file being used for this device.
	ROMFile string

	// DevNo identifies the ccw devices for s390x architecture
	DevNo string
}

// QemuParams returns the qemu parameters built out of the BalloonDevice.
func (b BalloonDevice) QemuParams(_ *Config) []string {
	var qemuParams []string
	var deviceParams []string

	driver := VirtioBalloon
	deviceParams = append(deviceParams, string(driver))

	if b.ID != "" {
		deviceParams = append(deviceParams, "id="+b.ID)
	}

	if isVirtioPCI[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf("romfile=%s", b.ROMFile))
	}

	if isVirtioCCW[driver] {
		deviceParams = append(deviceParams, fmt.Sprintf("devno=%s", b.DevNo))
	}

	if b.DeflateOnOOM {
		deviceParams = append(deviceParams, "deflate-on-oom=on")
	} else {
		deviceParams = append(deviceParams, "deflate-on-oom=off")
	}
	if s := driver.disableModern(b.DisableModern); s != "" {
		deviceParams = append(deviceParams, string(s))
	}
	qemuParams = append(qemuParams, "-device")
	qemuParams = append(qemuParams, strings.Join(deviceParams, ","))

	return qemuParams
}

// Valid returns true if the balloonDevice structure is valid and complete.
func (b BalloonDevice) Valid() bool {
	return b.ID != ""
}

// RTCBaseType is the qemu RTC base time type.
type RTCBaseType string

// RTCClock is the qemu RTC clock type.
type RTCClock string

// RTCDriftFix is the qemu RTC drift fix type.
type RTCDriftFix string

const (
	// UTC is the UTC base time for qemu RTC.
	UTC RTCBaseType = "utc"

	// LocalTime is the local base time for qemu RTC.
	LocalTime RTCBaseType = "localtime"
)

const (
	// Host is for using the host clock as a reference.
	Host RTCClock = "host"

	// VM is for using the guest clock as a reference
	VM RTCClock = "vm"
)

const (
	// Slew is the qemu RTC Drift fix mechanism.
	Slew RTCDriftFix = "slew"

	// NoDriftFix means we don't want/need to fix qemu's RTC drift.
	NoDriftFix RTCDriftFix = "none"
)

// RTC represents a qemu Real Time Clock configuration.
type RTC struct {
	// Base is the RTC start time.
	Base RTCBaseType

	// Clock is the is the RTC clock driver.
	Clock RTCClock

	// DriftFix is the drift fixing mechanism.
	DriftFix RTCDriftFix
}

// Valid returns true if the RTC structure is valid and complete.
func (rtc RTC) Valid() bool {
	if rtc.Clock != Host && rtc.Clock != VM {
		return false
	}

	if rtc.DriftFix != Slew && rtc.DriftFix != NoDriftFix {
		return false
	}

	return true
}

// QMPSocketType is the type of socket used for QMP communication.
type QMPSocketType string

const (
	// Unix socket for QMP.
	Unix QMPSocketType = "unix"
)

// QMPSocket represents a qemu QMP socket configuration.
type QMPSocket struct {
	// Type is the socket type (e.g. "unix").
	Type QMPSocketType

	// Name is the socket name.
	Name string

	// Server tells if this is a server socket.
	Server bool

	// NoWait tells if qemu should block waiting for a client to connect.
	NoWait bool
}

// Valid returns true if the QMPSocket structure is valid and complete.
func (qmp QMPSocket) Valid() bool {
	if qmp.Type == "" || qmp.Name == "" {
		return false
	}

	if qmp.Type != Unix {
		return false
	}

	return true
}

// SMP is the multi processors configuration structure.
type SMP struct {
	// CPUs is the number of VCPUs made available to qemu.
	CPUs uint32

	// Cores is the number of cores made available to qemu.
	Cores uint32

	// Threads is the number of threads made available to qemu.
	Threads uint32

	// Sockets is the number of sockets made available to qemu.
	Sockets uint32

	// MaxCPUs is the maximum number of VCPUs that a VM can have.
	// This value, if non-zero, MUST BE equal to or greater than CPUs
	MaxCPUs uint32
}

// Memory is the guest memory configuration structure.
type Memory struct {
	// Size is the amount of memory made available to the guest.
	// It should be suffixed with M or G for sizes in megabytes or
	// gigabytes respectively.
	Size string

	// Slots is the amount of memory slots made available to the guest.
	Slots uint8

	// MaxMem is the maximum amount of memory that can be made available
	// to the guest through e.g. hot pluggable memory.
	MaxMem string

	// Path is the file path of the memory device. It points to a local
	// file path used by FileBackedMem.
	Path string
}

// Kernel is the guest kernel configuration structure.
type Kernel struct {
	// Path is the guest kernel path on the host filesystem.
	Path string

	// InitrdPath is the guest initrd path on the host filesystem.
	InitrdPath string

	// Params is the kernel parameters string.
	Params string
}

// Knobs regroups a set of qemu boolean settings
type Knobs struct {
	// NoUserConfig prevents qemu from loading user config files.
	NoUserConfig bool

	// NoDefaults prevents qemu from creating default devices.
	NoDefaults bool

	// NoGraphic completely disables graphic output.
	NoGraphic bool

	// Daemonize will turn the qemu process into a daemon
	Daemonize bool

	// Both HugePages and MemPrealloc require the Memory.Size of the VM
	// to be set, as they need to reserve the memory upfront in order
	// for the VM to boot without errors.
	//
	// HugePages always results in memory pre-allocation.
	// However the setup is different from normal pre-allocation.
	// Hence HugePages has precedence over MemPrealloc
	// HugePages will pre-allocate all the RAM from huge pages
	HugePages bool

	// MemPrealloc will allocate all the RAM upfront
	MemPrealloc bool

	// FileBackedMem requires Memory.Size and Memory.Path of the VM to
	// be set.
	FileBackedMem bool

	// MemShared will set the memory device as shared.
	MemShared bool

	// Mlock will control locking of memory
	// Only active when Realtime is set to true
	Mlock bool

	// Stopped will not start guest CPU at startup
	Stopped bool

	// Realtime will enable realtime QEMU
	Realtime bool
}

// IOThread allows IO to be performed on a separate thread.
type IOThread struct {
	ID string
}

const (
	// MigrationFD is the migration incoming type based on open file descriptor.
	// Skip default 0 so that it must be set on purpose.
	MigrationFD = 1
	// MigrationExec is the migration incoming type based on commands.
	MigrationExec = 2
	// MigrationDefer is the defer incoming type
	MigrationDefer = 3
)

// Incoming controls migration source preparation
type Incoming struct {
	// Possible values are MigrationFD, MigrationExec
	MigrationType int
	// Only valid if MigrationType == MigrationFD
	FD *os.File
	// Only valid if MigrationType == MigrationExec
	Exec string
}

// Config is the qemu configuration structure.
// It allows for passing custom settings and parameters to the qemu API.
type Config struct {
	// Path is the qemu binary path.
	Path string

	// Ctx is the context used when launching qemu.
	Ctx context.Context

	// Name is the qemu guest name
	Name string

	// UUID is the qemu process UUID.
	UUID string

	// CPUModel is the CPU model to be used by qemu.
	CPUModel string

	// Machine
	Machine Machine

	// QMPSockets is a slice of QMP socket description.
	QMPSockets []QMPSocket

	// Devices is a list of devices for qemu to create and drive.
	Devices []Device

	// RTC is the qemu Real Time Clock configuration
	RTC RTC

	// VGA is the qemu VGA mode.
	VGA string

	// Kernel is the guest kernel configuration.
	Kernel Kernel

	// Memory is the guest memory configuration.
	Memory Memory

	// SMP is the quest multi processors configuration.
	SMP SMP

	// GlobalParam is the -global parameter.
	GlobalParam string

	// Knobs is a set of qemu boolean settings.
	Knobs Knobs

	// Bios is the -bios parameter
	Bios string

	// Incoming controls migration source preparation
	Incoming Incoming

	// fds is a list of open file descriptors to be passed to the spawned qemu process
	fds []*os.File

	IOThreads []IOThread

	// PidFile is the -pidfile parameter
	PidFile string

	// LogFile is the -D parameter
	LogFile string

	qemuParams []string
}

// appendFDs append a list of file descriptors to the qemu configuration and
// returns a slice of offset file descriptors that will be seen by the qemu process.
func (config *Config) appendFDs(fds []*os.File) []int {
	var fdInts []int

	oldLen := len(config.fds)

	config.fds = append(config.fds, fds...)

	// The magic 3 offset comes from https://golang.org/src/os/exec/exec.go:
	//     ExtraFiles specifies additional open files to be inherited by the
	//     new process. It does not include standard input, standard output, or
	//     standard error. If non-nil, entry i becomes file descriptor 3+i.
	for i := range fds {
		fdInts = append(fdInts, oldLen+3+i)
	}

	return fdInts
}

func (config *Config) appendName() {
	if config.Name != "" {
		config.qemuParams = append(config.qemuParams, "-name")
		config.qemuParams = append(config.qemuParams, config.Name)
	}
}

func (config *Config) appendMachine() {
	if config.Machine.Type != "" {
		var machineParams []string

		machineParams = append(machineParams, config.Machine.Type)

		if config.Machine.Acceleration != "" {
			machineParams = append(machineParams, fmt.Sprintf(",accel=%s", config.Machine.Acceleration))
		}

		if config.Machine.Options != "" {
			machineParams = append(machineParams, fmt.Sprintf(",%s", config.Machine.Options))
		}

		config.qemuParams = append(config.qemuParams, "-machine")
		config.qemuParams = append(config.qemuParams, strings.Join(machineParams, ""))
	}
}

func (config *Config) appendCPUModel() {
	if config.CPUModel != "" {
		config.qemuParams = append(config.qemuParams, "-cpu")
		config.qemuParams = append(config.qemuParams, config.CPUModel)
	}
}

func (config *Config) appendQMPSockets() {
	for _, q := range config.QMPSockets {
		if !q.Valid() {
			continue
		}

		qmpParams := append([]string{}, fmt.Sprintf("%s:", q.Type))
		qmpParams = append(qmpParams, q.Name)
		if q.Server {
			qmpParams = append(qmpParams, ",server")
			if q.NoWait {
				qmpParams = append(qmpParams, ",nowait")
			}
		}

		config.qemuParams = append(config.qemuParams, "-qmp")
		config.qemuParams = append(config.qemuParams, strings.Join(qmpParams, ""))
	}
}

func (config *Config) appendDevices() {
	for _, d := range config.Devices {
		if !d.Valid() {
			continue
		}

		config.qemuParams = append(config.qemuParams, d.QemuParams(config)...)
	}
}

func (config *Config) appendUUID() {
	if config.UUID != "" {
		config.qemuParams = append(config.qemuParams, "-uuid")
		config.qemuParams = append(config.qemuParams, config.UUID)
	}
}

func (config *Config) appendMemory() {
	if config.Memory.Size != "" {
		var memoryParams []string

		memoryParams = append(memoryParams, config.Memory.Size)

		if config.Memory.Slots > 0 {
			memoryParams = append(memoryParams, fmt.Sprintf(",slots=%d", config.Memory.Slots))
		}

		if config.Memory.MaxMem != "" {
			memoryParams = append(memoryParams, fmt.Sprintf(",maxmem=%s", config.Memory.MaxMem))
		}

		config.qemuParams = append(config.qemuParams, "-m")
		config.qemuParams = append(config.qemuParams, strings.Join(memoryParams, ""))
	}
}

func (config *Config) appendCPUs() error {
	if config.SMP.CPUs > 0 {
		var SMPParams []string

		SMPParams = append(SMPParams, fmt.Sprintf("%d", config.SMP.CPUs))

		if config.SMP.Cores > 0 {
			SMPParams = append(SMPParams, fmt.Sprintf(",cores=%d", config.SMP.Cores))
		}

		if config.SMP.Threads > 0 {
			SMPParams = append(SMPParams, fmt.Sprintf(",threads=%d", config.SMP.Threads))
		}

		if config.SMP.Sockets > 0 {
			SMPParams = append(SMPParams, fmt.Sprintf(",sockets=%d", config.SMP.Sockets))
		}

		if config.SMP.MaxCPUs > 0 {
			if config.SMP.MaxCPUs < config.SMP.CPUs {
				return fmt.Errorf("MaxCPUs %d must be equal to or greater than CPUs %d",
					config.SMP.MaxCPUs, config.SMP.CPUs)
			}
			SMPParams = append(SMPParams, fmt.Sprintf(",maxcpus=%d", config.SMP.MaxCPUs))
		}

		config.qemuParams = append(config.qemuParams, "-smp")
		config.qemuParams = append(config.qemuParams, strings.Join(SMPParams, ""))
	}

	return nil
}

func (config *Config) appendRTC() {
	if !config.RTC.Valid() {
		return
	}

	var RTCParams []string

	RTCParams = append(RTCParams, fmt.Sprintf("base=%s", string(config.RTC.Base)))

	if config.RTC.DriftFix != "" {
		RTCParams = append(RTCParams, fmt.Sprintf(",driftfix=%s", config.RTC.DriftFix))
	}

	if config.RTC.Clock != "" {
		RTCParams = append(RTCParams, fmt.Sprintf(",clock=%s", config.RTC.Clock))
	}

	config.qemuParams = append(config.qemuParams, "-rtc")
	config.qemuParams = append(config.qemuParams, strings.Join(RTCParams, ""))
}

func (config *Config) appendGlobalParam() {
	if config.GlobalParam != "" {
		config.qemuParams = append(config.qemuParams, "-global")
		config.qemuParams = append(config.qemuParams, config.GlobalParam)
	}
}

func (config *Config) appendVGA() {
	if config.VGA != "" {
		config.qemuParams = append(config.qemuParams, "-vga")
		config.qemuParams = append(config.qemuParams, config.VGA)
	}
}

func (config *Config) appendKernel() {
	if config.Kernel.Path != "" {
		config.qemuParams = append(config.qemuParams, "-kernel")
		config.qemuParams = append(config.qemuParams, config.Kernel.Path)

		if config.Kernel.InitrdPath != "" {
			config.qemuParams = append(config.qemuParams, "-initrd")
			config.qemuParams = append(config.qemuParams, config.Kernel.InitrdPath)
		}

		if config.Kernel.Params != "" {
			config.qemuParams = append(config.qemuParams, "-append")
			config.qemuParams = append(config.qemuParams, config.Kernel.Params)
		}
	}
}

func (config *Config) appendMemoryKnobs() {
	if config.Memory.Size == "" {
		return
	}
	if !isDimmSupported() {
		return
	}
	var objMemParam, numaMemParam string
	dimmName := "dimm1"
	if config.Knobs.HugePages {
		objMemParam = "memory-backend-file,id=" + dimmName + ",size=" + config.Memory.Size + ",mem-path=/dev/hugepages"
		numaMemParam = "node,memdev=" + dimmName
	} else if config.Knobs.FileBackedMem && config.Memory.Path != "" {
		objMemParam = "memory-backend-file,id=" + dimmName + ",size=" + config.Memory.Size + ",mem-path=" + config.Memory.Path
		numaMemParam = "node,memdev=" + dimmName
	} else {
		objMemParam = "memory-backend-ram,id=" + dimmName + ",size=" + config.Memory.Size
		numaMemParam = "node,memdev=" + dimmName
	}

	if config.Knobs.MemShared {
		objMemParam += ",share=on"
	}
	if config.Knobs.MemPrealloc {
		objMemParam += ",prealloc=on"
	}
	config.qemuParams = append(config.qemuParams, "-object")
	config.qemuParams = append(config.qemuParams, objMemParam)

	config.qemuParams = append(config.qemuParams, "-numa")
	config.qemuParams = append(config.qemuParams, numaMemParam)
}

func (config *Config) appendKnobs() {
	if config.Knobs.NoUserConfig {
		config.qemuParams = append(config.qemuParams, "-no-user-config")
	}

	if config.Knobs.NoDefaults {
		config.qemuParams = append(config.qemuParams, "-nodefaults")
	}

	if config.Knobs.NoGraphic {
		config.qemuParams = append(config.qemuParams, "-nographic")
	}

	if config.Knobs.Daemonize {
		config.qemuParams = append(config.qemuParams, "-daemonize")
	}

	config.appendMemoryKnobs()

	if config.Knobs.Realtime {
		config.qemuParams = append(config.qemuParams, "-realtime")
		// This path is redundant as the default behaviour is locked memory
		// Realtime today does not control any other feature even though
		// other features may be added in the future
		// https://lists.gnu.org/archive/html/qemu-devel/2012-12/msg03330.html
		if config.Knobs.Mlock {
			config.qemuParams = append(config.qemuParams, "mlock=on")
		} else {
			config.qemuParams = append(config.qemuParams, "mlock=off")
		}
	} else {
		// In order to turn mlock off we need the -realtime option as well
		if !config.Knobs.Mlock {
			//Enable realtime anyway just to get the right swapping behaviour
			config.qemuParams = append(config.qemuParams, "-realtime")
			config.qemuParams = append(config.qemuParams, "mlock=off")
		}
	}

	if config.Knobs.Stopped {
		config.qemuParams = append(config.qemuParams, "-S")
	}
}

func (config *Config) appendBios() {
	if config.Bios != "" {
		config.qemuParams = append(config.qemuParams, "-bios")
		config.qemuParams = append(config.qemuParams, config.Bios)
	}
}

func (config *Config) appendIOThreads() {
	for _, t := range config.IOThreads {
		if t.ID != "" {
			config.qemuParams = append(config.qemuParams, "-object")
			config.qemuParams = append(config.qemuParams, fmt.Sprintf("iothread,id=%s", t.ID))
		}
	}
}

func (config *Config) appendIncoming() {
	var uri string
	switch config.Incoming.MigrationType {
	case MigrationExec:
		uri = fmt.Sprintf("exec:%s", config.Incoming.Exec)
	case MigrationFD:
		chFDs := config.appendFDs([]*os.File{config.Incoming.FD})
		uri = fmt.Sprintf("fd:%d", chFDs[0])
	case MigrationDefer:
		uri = "defer"
	default:
		return
	}
	config.qemuParams = append(config.qemuParams, "-S", "-incoming", uri)
}

func (config *Config) appendPidFile() {
	if config.PidFile != "" {
		config.qemuParams = append(config.qemuParams, "-pidfile")
		config.qemuParams = append(config.qemuParams, config.PidFile)
	}
}

func (config *Config) appendLogFile() {
	if config.LogFile != "" {
		config.qemuParams = append(config.qemuParams, "-D")
		config.qemuParams = append(config.qemuParams, config.LogFile)
	}
}

// LaunchQemu can be used to launch a new qemu instance.
//
// The Config parameter contains a set of qemu parameters and settings.
//
// This function writes its log output via logger parameter.
//
// The function will block until the launched qemu process exits.  "", nil
// will be returned if the launch succeeds.  Otherwise a string containing
// the contents of stderr + a Go error object will be returned.
func LaunchQemu(config Config, logger QMPLog) (string, error) {
	config.appendName()
	config.appendUUID()
	config.appendMachine()
	config.appendCPUModel()
	config.appendQMPSockets()
	config.appendMemory()
	config.appendDevices()
	config.appendRTC()
	config.appendGlobalParam()
	config.appendVGA()
	config.appendKnobs()
	config.appendKernel()
	config.appendBios()
	config.appendIOThreads()
	config.appendIncoming()
	config.appendPidFile()
	config.appendLogFile()

	if err := config.appendCPUs(); err != nil {
		return "", err
	}

	ctx := config.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	return LaunchCustomQemu(ctx, config.Path, config.qemuParams,
		config.fds, nil, logger)
}

// LaunchCustomQemu can be used to launch a new qemu instance.
//
// The path parameter is used to pass the qemu executable path.
//
// params is a slice of options to pass to qemu-system-x86_64 and fds is a
// list of open file descriptors that are to be passed to the spawned qemu
// process.  The attrs parameter can be used to control aspects of the
// newly created qemu process, such as the user and group under which it
// runs.  It may be nil.
//
// This function writes its log output via logger parameter.
//
// The function will block until the launched qemu process exits.  "", nil
// will be returned if the launch succeeds.  Otherwise a string containing
// the contents of stderr + a Go error object will be returned.
func LaunchCustomQemu(ctx context.Context, path string, params []string, fds []*os.File,
	attr *syscall.SysProcAttr, logger QMPLog) (string, error) {
	if logger == nil {
		logger = qmpNullLogger{}
	}

	errStr := ""

	if path == "" {
		path = "qemu-system-x86_64"
	}

	/* #nosec */
	cmd := exec.CommandContext(ctx, path, params...)
	if len(fds) > 0 {
		logger.Infof("Adding extra file %v", fds)
		cmd.ExtraFiles = fds
	}

	cmd.SysProcAttr = attr
	if attr == nil {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		uid, err := strconv.ParseInt(u.Uid, 0, 32)
		if err != nil {
			return "", err
		}
		gid, err := strconv.ParseInt(u.Gid, 0, 32)
		if err != nil {
			return "", err
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{
				Uid: uint32(uid),
				Gid: uint32(gid),
			},
			AmbientCaps: []uintptr{12/*CAP_NET_ADMIN*/},
		}
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	logger.Infof("launching %s with: %v", path, params)

	err := cmd.Run()
	if err != nil {
		logger.Errorf("Unable to launch %s: %v", path, err)
		errStr = stderr.String()
		logger.Errorf("%s", errStr)
	}
	return errStr, err
}
