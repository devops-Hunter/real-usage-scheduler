package cpu_real_usage

import (
	"context"
	"fmt"
	"github.com/prometheus/client_golang/api"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"math"
	"sigs.k8s.io/scheduler-plugins/apis/config"
	"sync"
	"time"
)

// Name is the name of the plugin used in the Registry and configurations.
const Name = "CpuRealUsage"

type CpuRealUsage struct {
	handle                framework.Handle
	MetricsConfig         *config.CpuRealUsageArgs //此插件的参数
	RealUsageMetricsCache sync.Map                 //缓存所有节点的真实负载值的cache map
}

// Name 定义一个Name方法，用来实现framework.Plugin接口
func (cru *CpuRealUsage) Name() string {
	return Name
}

// New initializes a new plugin and returns it.
func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	args, ok := obj.(*config.CpuRealUsageArgs)
	if !ok {
		err := fmt.Errorf("wrong args for plugin.CpuRealUsage.New.runtime.Object.config.CpuRealUsageArgs: %T", obj)
		klog.Errorf("plugin.CpuRealUsage.New.runtime.Object.config.CpuRealUsageArgs.err: %T", obj)
		return nil, err
	}
	cru := &CpuRealUsage{
		handle:                h,
		RealUsageMetricsCache: sync.Map{},
		MetricsConfig:         args,
	}
	ctx := context.Background()
	go wait.Until(cru.GetMetricsData, time.Second*time.Duration(cru.MetricsConfig.QueryMetricIntervalSeconds), ctx.Done())

	return cru, nil
}

// UsagePromql 定义metrics 预聚合的方法
const UsagePromql = `node_cpu_usage_avg_5m`

// GetMetricsData 查询prometheus获取指标的方法
func (cru *CpuRealUsage) GetMetricsData() {
	// 调用prometheus sdk的instance query方法
	// 先初始化一个prometheus client
	address := cru.MetricsConfig.PrometheusApiAddr
	if len(address) == 0 {
		return
	}
	klog.Infof("CpuRealUsage Start; GetMetricsData.PrometheusApiAddr: %v", address)
	client, err := api.NewClient(api.Config{
		Address: cru.MetricsConfig.PrometheusApiAddr,
	})
	if err != nil {
		klog.Errorf("GetMetricsData.New.PrometheusClient.err: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(cru.MetricsConfig.QueryMetricTimeSeconds))

	defer cancel()

	//构造query查询
	v1Api := prometheusv1.NewAPI(client)
	// 处理promql 的error
	queryStr := UsagePromql
	klog.Infof("Query prometheus by: %s", queryStr)
	res, warnings, err := v1Api.Query(ctx, queryStr, time.Now())
	if err != nil {
		klog.Errorf("Error querying Prometheus: %v", err)
		return
	}
	if len(warnings) > 0 {
		klog.V(3).Infof("Warnings querying Prometheus: %v", warnings)
		return
	}

	if res == nil || res.String() == "" {
		klog.Warningf("Warning querying Prometheus: no data found for %s", queryStr)
		return
	}
	// plugin.usage.only need type pmodel.ValVector in
	vec, ok := res.(model.Vector)
	if !ok {
		klog.Errorf("[queryOneMetrics.querying.Prometheus.result.to.vector.err][result:%+v]", vec)
		return
	}

	// 做遍历
	for _, v := range vec {
		v := v
		// 注意prometheus需要打`node`标签
		nodeName := string(v.Metric["node"])
		value := float64(v.Value)
		klog.Infof("CpuRealUsage.GetMetricsData node: %v, value: %v, Promql:%v", nodeName, value, queryStr)
		cru.RealUsageMetricsCache.Store(nodeName, value)
	}

	klog.Infof("CpuRealUsage End; GetMetricsData.PrometheusApiAddr: %v", address)

}

// 注册打分
var _ = framework.ScorePlugin(&CpuRealUsage{})

// Score invoked at the score extension point.
func (cru *CpuRealUsage) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	// 根据nodeName 从监控数据缓存中拿到真实负载数据 return
	value, ok := cru.RealUsageMetricsCache.Load(nodeName)
	if !ok {
		klog.Errorf("CpuRealUsage.Score.RealUsageMetricsCache.Load.notFound node: %v", nodeName)
		return 0, framework.NewStatus(framework.Error, fmt.Sprintf("getting node %v from RealUsageMetricCache", nodeName))
	}
	loadV := int64(value.(float64))
	klog.Infof("CpuRealUsage.Score.RealUsageMetricsCache.Load.Found node: %v, value: %v", nodeName, loadV)

	return loadV, nil

}

// ScoreExtensions of the Score plugin.
func (cru *CpuRealUsage) ScoreExtensions() framework.ScoreExtensions {
	return cru
}

// NormalizeScore  打分逻辑函数
func (cru *CpuRealUsage) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	// Find highest and lowest scores.
	var highest int64 = -math.MaxInt64
	var lowest int64 = math.MaxInt64
	for _, nodeScore := range scores {
		if nodeScore.Score > highest {
			highest = nodeScore.Score
		}
		if nodeScore.Score < lowest {
			lowest = nodeScore.Score
		}
	}

	// Transform the highest to lowest score range to fit the framework's min to max node score range.
	oldRange := highest - lowest
	newRange := framework.MaxNodeScore - framework.MinNodeScore
	for i, nodeScore := range scores {
		if oldRange == 0 {
			scores[i].Score = framework.MinNodeScore
			klog.Infof("[CpuRealUsage.NormalizeScore.noNeedAdr][node: %v][originScore: %v]", nodeScore.Name, nodeScore.Score)
		} else {
			thisScore := +framework.MaxNodeScore - ((nodeScore.Score - lowest) * newRange / oldRange)
			//scores[i].Score = ((nodeScore.Score - lowest) * newRange / oldRange) + framework.MinNodeScore

			klog.Infof("[CpuRealUsage.NormalizeScore.Print][node: %v][originScore: %v][thisScore: %v]", nodeScore.Name, nodeScore.Score, thisScore)
			scores[i].Score = thisScore
		}
	}

	/*
			原始评分映射举栗子,三个节点原始分数为:
		    {"nodeA": 150, "nodeB": 250, "nodeC": 350}
			oldRange = 350 - 150 = 200
			newRange = 100 - 0 = 100
			nodeA: (150-150) * 100 / 200 + 0 = 0   因此分数变化 150 -> 0
			nodeB: (250-150) * 100 / 200 + 0 = 50  因此分数变化 250 -> 50
			nodeC: (350-150) * 100 / 200 + 0 = 100 因此分数变化 350 -> 100
	*/
	// m3: 实现符合我们场景的评分映射逻辑
	/*
			本次代码评分映射后,三个节点原始分数为:
		    {"nodeA": 150, "nodeB": 250, "nodeC": 350}
			oldRange = 350 - 150 = 200
			newRange = 100 - 0 = 100
			nodeA: 100 - (150-150) * 100 / 200 = 100   因此分数变化 150 -> 100
			nodeB: 100 - (250-150) * 100 / 200 = 50    因此分数变化 250 -> 50
			nodeC: 100 - (350-150) * 100 / 200 = 0     因此分数变化 350 -> 0
	*/

	return nil
}
