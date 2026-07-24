package helpercontract

import "time"

const (
	// RuntimeNativeReadinessTimeout bounds native presentation startup readiness.
	RuntimeNativeReadinessTimeout = 30 * time.Second
	// RuntimeCatalogReadinessTimeout bounds catalog worker startup readiness.
	RuntimeCatalogReadinessTimeout = 30 * time.Second
	// RuntimeCatalogOperationTimeout bounds each catalog worker operation.
	RuntimeCatalogOperationTimeout = 30 * time.Second
	// RuntimeShutdownTimeout bounds runtime and worker shutdown.
	RuntimeShutdownTimeout = 5 * time.Second
)
