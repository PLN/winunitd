package version

// Product MSI identity. These values are recorded in source and copied into
// packaging/wix. Do not regenerate them at build time. A new major-upgrade
// release needs a new ProductCode; UpgradeCode stays constant for the x64
// product. PackageCode is a per-build MSI identifier and is not listed here.
const (
	// InstallerVersion is the MSI ProductVersion paired with Version.
	// Windows Installer orders only the first three numeric fields, so the
	// human prerelease suffix is not part of this value.
	InstallerVersion = "0.1.0"

	ProductName  = "WinUnit Manager"
	Manufacturer = "PLN"
	Copyright    = "Copyright (c) 2026 PLN"
	SupportURL   = "https://github.com/PLN/winunitd"

	UpgradeCode = "A512B91F-1883-40FD-8EDB-5B8C5708DEEA"
	ProductCode = "78C43374-5AB7-4E81-B9CF-09E8ACD01133"

	// BetaUpgradeCode is the shipped beta package (packaging/beta). The
	// product MSI only detects it. It is not this product's upgrade identity.
	BetaUpgradeCode = "9443AE50-251B-4A46-9465-B835A3A27133"
)

// Component is one stable MSI component identity.
type Component struct {
	ID   string
	GUID string
}

// Components are the product MSI components. GUIDs stay stable while the
// component's key path identifies the same resource.
var Components = []Component{
	{ID: "InstallDir", GUID: "83090082-C8E6-4AB0-A15E-F251BE295A2F"},
	{ID: "WinunitdExe", GUID: "72FE81DD-D07E-4666-8ED4-315C669ABA87"},
	{ID: "WinctlExe", GUID: "2704F5FB-2312-415C-AA67-0B9AC0A27EA6"},
	{ID: "NotifyExe", GUID: "CFB4D2E3-0013-43C8-8731-B8078E842F1A"},
	{ID: "License", GUID: "BAAB831A-A41F-4626-A2C6-0AD9A040D1E1"},
	{ID: "Notices", GUID: "32936E62-9823-46CA-A7F6-2AAF3801BAD5"},
	{ID: "InstallDoc", GUID: "3244160C-AE3D-4ACF-9F72-206EC28A40A2"},
	{ID: "UnitReference", GUID: "CF528EB2-7B51-4C0F-9C3E-313003819CD1"},
	{ID: "ExampleService", GUID: "9E2A318D-032C-4116-A51A-89268C5560F8"},
	{ID: "ExampleTarget", GUID: "C0E86A03-4BD0-4DFB-B854-CC540DABF0BC"},
	{ID: "ExampleReadme", GUID: "12ECF973-B33E-4C4C-BC89-8E8C704FE461"},
	{ID: "EventSource", GUID: "CF160772-EA1F-4378-AA17-BF74519B1EEA"},
	{ID: "PathEntry", GUID: "02E7B71D-5227-4CF6-BEC4-2A2F7FCD6D0E"},
	{ID: "DataRoot", GUID: "DB86659B-378D-4625-9191-8F5A60A56C20"},
	{ID: "DataUnits", GUID: "4FD46D56-BD2D-4D61-B3A1-8C2085CA6049"},
	{ID: "DataEnabled", GUID: "C82AEC80-3F19-447B-8274-96C5528DC063"},
	{ID: "DataJournal", GUID: "E74C44F8-648B-4FF0-9C60-5F146E070101"},
	{ID: "DataRuntime", GUID: "637F893D-37CD-448A-B532-E02DA2BBF5EA"},
	{ID: "DataLinger", GUID: "C9F8A0BD-7AB7-4A29-8522-751CA03D2309"},
	{ID: "DataDaemon", GUID: "99718886-3A26-46B3-9F29-4EA42ECADFBF"},
}
