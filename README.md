`kubectl-check-ownerreferences` is a read-only tool that identifies objects with potentially 
problematic items in `metadata.ownerReferences`. See http://issue.k8s.io/65200
and http://issue.k8s.io/92743 for more context.

**To install:**

```sh
git clone https://github.com/kubernetes-sigs/kubectl-check-ownerreferences.git
cd kubectl-check-ownerreferences
make install
```

**To use:**

1. Ensure kubectl can speak to the cluster you want to check:

```sh
kubectl version
```

> ```sh
> Client Version: ...
> Server Version: ...
> ```

2. Invoke `kubectl-check-ownerreferences`, and it will read items from the same cluster as `kubectl`:

```sh
kubectl-check-ownerreferences 
```

> ```
> No invalid ownerReferences found
> ```

**Details**

`kubectl-check-ownerreferences` does the following:
1. Discovers available resources in your cluster
2. Lists the metadata for each resource, building a set of existing objects in the cluster
3. Sweeps the `ownerReferences` for existing objects, and makes sure the referenced owners:
   1. exist
   2. have a matching kind
   3. have a matching name
   4. are in the correct namespace (or are cluster-scoped)
   5. are referenced via a resolveable `apiVersion`

**Error handling**

If some resources cannot be discovered or listed,
`kubectl-check-ownerreferences` will output warnings to `stderr` and continue.

If some child objects have ownerReferences that refer to the
undiscoverable or unlistable resources, warnings will be printed to `stderr`.

If parent objects are deleted or child objects are created
while `kubectl-check-ownerreferences` is running, false positives can be reported.

**Options**

* Output machine-readable results to `stdout` with `-o json`

* Increase verbosity with `--v` (levels 2-9) to see more details about the requests being made

* Increase or decrease the speed with which API requests are made with `--qps` and `--burst`
