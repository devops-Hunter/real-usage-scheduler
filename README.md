

## 部署文档

### 在K8S上一把梭

> list
> - 一个K8S集群
> 	- 1.24以上，低于这个版本没测过
> - prometheus
> 	- 需要在node-exporter上新增一个label标签"node"
> 	- 新增一个rule规则用于聚合cpu/memory指标
> - 调度器默认使用的kube-config文件
> 	- master节点有/etc/kubernetes/scheduler.conf
> 	- 公有云托管版的话自己生成一个config

#### 首先配置一下prometheus

- 每台节点上加一个node的标签区分节点。这里就不赘述怎么做了

<img width="1488" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/f38ea5f9-0198-4a7b-b771-5f7c9281ae81">

- 加一个聚合查询负载情况，先算顺时值，然后横向聚合一下
```yaml
groups:
  - name: for-usage-scheduler
	rules:
	- record: node_cpu_usage_avg_5m
	  expr: avg_over_time((avg(rate(node_cpu_seconds_total{mode ="user"}[1m])) by (instance) * 100) [5m:1m])
	- record: node_mem_usage_avg_5m
	  expr: avg_over_time( ((1- (node_memory_Buffers_bytes + node_memory_Cached_bytes + node_memory_MemFree_bytes)  / node_memory_MemTotal_bytes ) * 100 )[5m:1m])
```
<img width="1519" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/2700612c-7d66-45da-a754-b65abc19d974">



#### 创建配置文件

> 实际上就是一个载体用来存kubeconfig和scheduling crd配置文件。这里我们用secret

- 把/etc/kubernetes/scheduler.conf取名为config放在本地路径下

- 创建一个调度器的crd配置保存为MemRealUsage-scheduler-conf.yaml（以memory为例子）
```yaml
apiVersion: kubescheduler.config.k8s.io/v1                                                                                                                  
kind: KubeSchedulerConfiguration                                                                                                                            
leaderElection:                                                                                                                                             
  leaderElect: false                                                                                                                                        
clientConnection:                                                                                                                                           
  kubeconfig: "/etc/kubernetes/scheduler.conf"                                                                                                              
profiles:                                                                                                                                                   
- schedulerName: mem-real-usage-scheduler                                                                                                                   
  plugins:                                                                                                                                                  
    score:                                                                                                                                                  
      enabled:                                                                                                                                              
      - name: MemRealUsage                                                                                                                                  
      disabled:                                                                                                                                             
      - name: "*"                                                                                                                                           
# optional plugin configs                                                                                                                                   
  pluginConfig:                                                                                                                                             
  - name: MemRealUsage                                                                                                                                      
    args:                                                                                                                                                   
      prometheusApiAddr: http://10.96.0.102:9090  #这里填自己的prometheus地址                                                                                       
      queryMetricTimeSeconds: 10                                                                                                                            
      queryMetricIntervalSeconds: 10
```

- 创建secret
```shell
kubectl create secret generic kube-scheduler-mem-secret  \
--from-file=scheduler.conf=config  \
--from-file=MemRealUsage-scheduler-conf.yaml=MemRealUsage-scheduler-conf.yaml  \
-n kube-system
```


#### 部署deployment
- 准备all-in-one.yaml
```yaml
# First part
# Apply extra privileges to system:kube-scheduler.
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: system:kube-scheduler:plugins
rules:
- apiGroups: ["scheduling.x-k8s.io"]
  resources: ["podgroups", "elasticquotas", "podgroups/status", "elasticquotas/status"]
  verbs: ["get", "list", "watch", "create", "delete", "update", "patch"]
# for network-aware plugins add the following lines (scheduler-plugins v.0.24.9)
#- apiGroups: [ "appgroup.diktyo.k8s.io" ]
#  resources: [ "appgroups" ]
#  verbs: [ "get", "list", "watch", "create", "delete", "update", "patch" ]
#- apiGroups: [ "networktopology.diktyo.k8s.io" ]
#  resources: [ "networktopologies" ]
#  verbs: [ "get", "list", "watch", "create", "delete", "update", "patch" ]
#- apiGroups: ["security-profiles-operator.x-k8s.io"]
#  resources: ["seccompprofiles", "profilebindings"]
#  verbs: ["get", "list", "watch", "create", "delete", "update", "patch"]
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: system:kube-scheduler:plugins
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:kube-scheduler:plugins
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: User
  name: system:kube-scheduler
---
# Second part
# Install the controller image.
apiVersion: v1
kind: Namespace
metadata:
  name: scheduler-plugins
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: scheduler-plugins-controller
  namespace: scheduler-plugins
---
kind: ClusterRole
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: scheduler-plugins-controller
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["scheduling.x-k8s.io"]
  resources: ["podgroups", "elasticquotas", "podgroups/status", "elasticquotas/status"]
  verbs: ["get", "list", "watch", "create", "delete", "update", "patch"]
- apiGroups: [""]
  resources: ["events"]
  verbs: ["create", "patch", "update"]
#- apiGroups: ["security-profiles-operator.x-k8s.io"]
#  resources: ["seccompprofiles", "profilebindings"]
#  verbs: ["get", "list", "watch", "create", "delete", "update", "patch"]
---
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: scheduler-plugins-controller
subjects:
- kind: ServiceAccount
  name: scheduler-plugins-controller
  namespace: scheduler-plugins
roleRef:
  kind: ClusterRole
  name: scheduler-plugins-controller
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: schedulingplugin-mem-usage
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      component: scheduler
      tier: control-plane
  template:
    metadata:
      labels:
        component: scheduler
        tier: control-plane
    spec:
      # nodeSelector:
      #   node-role.kubernetes.io/control-plane: ""
      containers:
        - image: registry.cn-shanghai.aliyuncs.com/devopsn/kube-scheduler:latest
          # imagePullPolicy: Never
          command:
          - /bin/kube-scheduler
          - --authentication-kubeconfig=/etc/kubernetes/scheduler.conf
          - --authorization-kubeconfig=/etc/kubernetes/scheduler.conf
          - --config=/etc/kubernetes/MemRealUsage-scheduler-conf.yaml
          name: schedulingplugin
          securityContext:
            privileged: true
          volumeMounts:
          - mountPath: /etc/kubernetes
            name: kube-scheduler-mem-secret-volume
      hostNetwork: false
      hostPID: false
      volumes:
      - name: kube-scheduler-mem-secret-volume
        secret:
          secretName: kube-scheduler-mem-secret
```

==**如果是部署在master节点上可以直接使用hostPath，详情见文档[all-in-one](https://github.com/kubernetes-sigs/scheduler-plugins/blob/master/doc/develop.md)。我这里因为是公有云托管版所以使用这种方式部署**==

#### 测试demo

- 建一个deployment在`spec.template.spec.schedulerName`选择我们的scheduler测试一下
```yaml
kind: Deployment
apiVersion: apps/v1
metadata:
  name: nginx-mem-scheduler
  namespace: default
  labels:
    app: nginx
spec:
  replicas: 30
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
        - name: nginx
          image: nginx
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 80
          resources:
            requests:
              cpu: "50m"
              memory: "100Mi"
      schedulerName: mem-real-usage-scheduler
```


- 先看看默认的调度情况
<img width="1137" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/f0f1575a-67f0-4631-b376-2d4d05ea377a">


- 我们来压测一下内存
```go
package main
import (
	"fmt"
	"time"
	"unsafe"
)

func memUse() {
	arr := [128 * 1024 * 1024]int64{}
	fmt.Printf("[index:%v][size:%v]\n", unsafe.Sizeof(arr))

	for i := 0; i < len(arr); i++ {
		arr[i] = 0
	}
	time.Sleep(time.Hour)
}

func main() {
	for i := 0; i < 6; i++ {
		go memUse()
	}
	time.Sleep(time.Hour)

}
```


- 在k8s-node-02上执行`go run main.go`观察一下prometheus监控曲线

<img width="1037" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/187c17b7-3222-4bf2-aaca-724a5091323c">

- 看下此时k8s-node-02的request
<img width="1264" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/cee44455-017d-4dca-affd-d2c7f786b8af">

- 此时再重启一下deployment看下调度情况
```shell
kubectl rollout restart deployment/nginx-mem-scheduler
kubectl get pods
```
<img width="1196" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/0487f69a-a8f2-4197-a2b0-7c2f68e56c85">

- 日志
<img width="611" alt="image" src="https://github.com/devops-Hunter/real-usage-scheduler/assets/59683023/cf7b260d-5d22-4b34-84d2-2ad4eade6497">



>  结论
> - 可以看到k8s-node-02上的request还相当充裕,此时的原始score达到了33分(其余节点分别为13,13)
> - 但是经过我们的调度器追加评分后，由于内存过高，直接从33分-->0分。可以看到后续pod对象绑定的节点都排除了k8s-node-02

>  TODO
> - 后续可以尝试一下节点资源超卖类型的调度器
> 	- 比如原先节点的内存request负载是80%，但是由于真实负载很低。我让它变成50%。这样score评分就变相提高了。可以让更多pod优先调度


### 源码编译

> warning
> - 一定要在liunx上编译。会调用一部分C库，本机上会出现很多奇怪的报错

1. clone源码
2. 编译
```shell
go env -w GO111MODULE=on && export GOPROXY=https://goproxy.cn,direct&& hack/update-codegen.sh
```

```shell
go env -w GO111MODULE=on && export GOPROXY=https://goproxy.cn,direct &&  make
```

3. 打包镜像`Dockerfile`
```dockerfile
FROM amd64/alpine:3.12                                                                                                                                      
COPY kube-scheduler  /bin/kube-scheduler                                                                                                                    
WORKDIR /bin                                                                                                                                                

CMD ["kube-scheduler"]
```

