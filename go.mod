module github.com/openshift-splat-team/fake-center

go 1.25.0

replace github.com/vmware/govmomi => github.com/openshift-splat-team/govmomi v0.0.0-20261013151717-dc14af853455

replace github.com/openshift-splat-team/fake-center/helper => ./helper

require (
	github.com/openshift-splat-team/fake-center/helper v0.0.0
	github.com/vmware/govmomi v0.56.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/google/uuid v1.6.0 // indirect
