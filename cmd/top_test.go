package cmd

import (
	"k8s.io/client-go/kubernetes"
)

// Compile-time assertion: gather must accept *kubernetes.Clientset.
// If gather were reverted to call k8s.NewClient() internally (the bug),
// it would no longer have this parameter and this file would not compile.
var _ func(*kubernetes.Clientset) (*dashboard, error) = gather
