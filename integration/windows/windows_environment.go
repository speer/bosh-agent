package windows

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/masterzen/winrm"

	. "github.com/onsi/gomega"
)

const dataDir = `C:\var\vcap\data\`

type WindowsEnvironment struct {
	Client *winrm.Client
}

func (e *WindowsEnvironment) ShrinkRootPartition() {
	e.RunPowershellCommandWithOffset(
		1,
		"Get-Partition -DriveLetter C | Resize-Partition -Size $(Get-PartitionSupportedSize -DriveLetter C).SizeMin",
	)
}

func (e *WindowsEnvironment) EnsureRootPartitionAtMaxSize() {
	freeSpaceOutput := e.RunPowershellCommandWithOffset(
		1,
		"Get-Disk $(Get-Partition -DriveLetter C | Select -ExpandProperty DiskNumber) | Select -ExpandProperty LargestFreeExtent",
	)

	freeSpace, err := strconv.Atoi(strings.TrimSpace(freeSpaceOutput))
	ExpectWithOffset(1, err).NotTo(HaveOccurred())

	if freeSpace > 0 {
		e.RunPowershellCommandWithOffset(
			1,
			"Resize-Partition -DriveLetter C -Size $(Get-PartitionSupportedSize -DriveLetter C).SizeMax",
		)
	}
}

func (e *WindowsEnvironment) GetDataDirPartitionNumber() string {
	return strings.TrimSpace(e.RunPowershellCommandWithOffset(
		1,
		fmt.Sprintf(`Get-Partition | Where AccessPaths -Contains "%s" | Select -ExpandProperty PartitionNumber`, dataDir),
	))
}

func (e *WindowsEnvironment) PartitionWithDataDirExists(diskNumber string) bool {
	return e.PartitionWithDataDirExistsWithOffset(1, diskNumber)
}

func (e *WindowsEnvironment) PartitionWithDataDirExistsWithOffset(offset int, diskNumber string) bool {
	stdout := e.RunPowershellCommandWithOffset(
		offset+1,
		fmt.Sprintf(
			`Get-Partition | where AccessPaths -Contains "%s" | Select -ExpandProperty DiskNumber`,
			dataDir,
		),
	)

	return strings.TrimSpace(stdout) == diskNumber
}

func (e *WindowsEnvironment) PartitionWithDataDirExistsFuncWithOffset(offset int, diskNumber string) func() bool {
	return func() bool {
		return e.PartitionWithDataDirExistsWithOffset(offset+1, diskNumber)
	}
}

func (e *WindowsEnvironment) PartitionWithDataDirExistsFunc(diskNumber string) func() bool {
	return e.PartitionWithDataDirExistsFuncWithOffset(1, diskNumber)
}

func (e *WindowsEnvironment) EnsureVolumeHasDataDir(diskNumber string) {
	EventuallyWithOffset(1, e.PartitionWithDataDirExistsFunc(diskNumber), 2*time.Minute).Should(
		BeTrue(),
		fmt.Sprintf(`Expected partition with access path %s to be present on disk %s`, dataDir, diskNumber),
	)
}

func (e *WindowsEnvironment) EnsureAgentServiceStopped() {
	stdout := e.RunPowershellCommandWithOffset(1, "Get-Service -Name bosh-agent | Format-List -Property Status")

	running, err := regexp.MatchString("Running", stdout)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	if running {
		e.RunPowershellCommandWithOffset(1, "c:\\bosh\\service_wrapper.exe stop")
	}
}

func (e *WindowsEnvironment) EnsureDataDirDoesntExist() {
	testPathOutput := e.RunPowershellCommandWithOffset(1, "Test-Path -Path %s", dataDir)

	exists := strings.TrimSpace(testPathOutput) == "True"
	if exists {
		e.RunPowershellCommandWithOffset(1, "Remove-Item %s -Force -Recurse", dataDir)
	}
}

func (e *WindowsEnvironment) AgentProcessRunningFunc() func() bool {
	return func() bool {
		exitCode, err := e.Client.Run(
			winrm.Powershell("Get-Process -ProcessName bosh-agent"),
			ioutil.Discard, ioutil.Discard,
		)
		return exitCode == 0 && err == nil
	}
}

func (e *WindowsEnvironment) RunPowershellCommandWithOffset(offset int, cmd string, cmdFmtArgs ...interface{}) string {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	exitCode, err := e.Client.Run(winrm.Powershell(fmt.Sprintf(cmd, cmdFmtArgs...)), stdout, stderr)

	outString := stdout.String()
	errString := stderr.String()

	ExpectWithOffset(offset+1, err).NotTo(
		HaveOccurred(),
		fmt.Sprintf(`Command "%s" failed with stdout: %s; stderr: %s`, cmd, outString, errString),
	)
	ExpectWithOffset(offset+1, exitCode).To(
		BeZero(),
		fmt.Sprintf(
			`Command "%s" failed with exit code: %d; stdout: %s; stderr: %s`,
			cmd,
			exitCode,
			stdout.String(),
			stderr.String(),
		),
	)

	return outString
}

func (e *WindowsEnvironment) RunPowershellCommand(cmd string, cmdFmtArgs ...interface{}) string {
	return e.RunPowershellCommandWithOffset(1, cmd, cmdFmtArgs...)
}

func (e *WindowsEnvironment) AssertDataACLed() {
	testFile := filepath.Join(dataDir + "testfile")

	e.RunPowershellCommandWithOffset(1, "echo 'content' >> %s", testFile)
	checkACLsOutput := e.RunPowershellCommandWithOffset(1, "Check-Acls %s", dataDir)
	aclErrsCount := strings.Count(checkACLsOutput, "Error")
	ExpectWithOffset(1, aclErrsCount == 0).To(
		BeTrue(),
		fmt.Sprintf("Expected data directory to have correct ACLs. Counted %d errors.", aclErrsCount),
	)
}

func (e *WindowsEnvironment) EnsureDiskClearedWithOffset(offset int, diskNumber string) {
	partitionCountOutput := e.RunPowershellCommandWithOffset(
		offset+1,
		"Get-Disk -Number %s | Select -ExpandProperty NumberOfPartitions",
		diskNumber,
	)

	partitionCount, err := strconv.Atoi(strings.TrimSpace(partitionCountOutput))
	ExpectWithOffset(offset+1, err).NotTo(HaveOccurred())
	if partitionCount > 0 {
		e.RunPowershellCommandWithOffset(offset+1, "Clear-Disk -Number %s -Confirm:$false -RemoveData", diskNumber)
	}
}

func (e *WindowsEnvironment) EnsureDiskCleared(diskNumber string) {
	e.EnsureDiskClearedWithOffset(1, diskNumber)
}
