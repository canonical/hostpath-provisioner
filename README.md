## Usage

[Helm](https://helm.sh) must be installed to use the charts.  Please refer to
Helm's [documentation](https://helm.sh/docs) to get started.

Once Helm has been set up correctly, you can add the repo as follows:
```
helm repo add hostpath-provisioner https://canonical.github.io/hostpath-provisioner
```

If you had already added this repo earlier, run `helm repo update` to retrieve
the latest versions of the package.

To install the `hostpath-provisioner` chart:

```
helm install hostpath-provisioner hostpath-provisioner/hostpath-provisioner -n kube-system
```

#### To install without adding the chart repository:
```
helm install hostpath-provisioner \
    --repo https://canonical.github.io/hostpath-provisioner \
    hostpath-provisioner \
    --namespace kube-system
```

#### To uninstall the chart:
```
helm delete hostpath-provisioner
```