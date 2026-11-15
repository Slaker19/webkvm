package firewall

// testClusterProfile uses TEST-NET ranges, never the lab addresses.
func testClusterProfile() ClusterProfile {
	p := DefaultClusterProfile()
	p.ClusterCIDR = "10.20.30.0/24"
	p.ManagementCIDRs = []string{"10.20.30.0/24", "203.0.113.0/24"}
	return p
}
