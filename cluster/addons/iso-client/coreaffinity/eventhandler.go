package coreaffinity

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"golang.org/x/net/context"

	"github.com/golang/glog"
	"google.golang.org/grpc"
	"k8s.io/kubernetes/cluster/addons/iso-client/cputopology"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/kubelet/api/v1alpha1/lifecycle"
	"strconv"
	"strings"
)

const (
	cgroupPrefix = "/sys/fs/cgroup/cpuset"
	cgroupSufix  = "/cpuset.cpus"
)

type EventHandler interface {
	RegisterEventHandler() error
	Serve(sync.WaitGroup)
}

type eventHandler struct {
	Name       string
	Address    string
	GrpcServer *grpc.Server
	Socket     net.Listener

	CPUTopology *cputopology.CPUTopology
}

// Constructor for EventHandler
func NewEventHandler(eventHandlerName string, eventHandlerAddress string, topology *cputopology.CPUTopology) *eventHandler {

	return &eventHandler{
		Name:        eventHandlerName,
		Address:     eventHandlerAddress,
		CPUTopology: topology,
	}
}

// Registering eventHandler server
func (e *eventHandler) RegisterEventHandler() (err error) {
	e.Socket, err = net.Listen("tcp", e.Address)
	if err != nil {
		return fmt.Errorf("Failed to bind to socket address: %v", err)
	}
	e.GrpcServer = grpc.NewServer()

	lifecycle.RegisterEventHandlerServer(e.GrpcServer, e)
	glog.Info("EvenHandler Server has been registered")

	return nil

}

// Start serving Grpc
func (e *eventHandler) Serve(wg sync.WaitGroup) {
	defer wg.Done()
	glog.Info("Starting serving")
	if err := e.GrpcServer.Serve(e.Socket); err != nil {
		glog.Fatalf("EventHandler server stopped serving : %v", err)
	}
	glog.Info("Stopping eventHandlerServer")
}

// isolation api
type isoSpec struct {
	CoreAffinity string `json:"core-affinity"`
}

// extract Pod object from Event
func getPod(bytePod []byte) (pod *api.Pod, err error) {
	pod = &api.Pod{}
	err = json.Unmarshal(bytePod, pod)
	if err != nil {
		glog.Fatalf("Cannot Unmarshal pod: %v", err)
		return
	}
	return
}

func (e *eventHandler) gatherContainerRequest(container api.Container) int64 {
	resource, ok := container.Resources.Requests[api.OpaqueIntResourceName(e.Name)]
	if !ok {
		return 0
	}
	return resource.Value()
}

func (e *eventHandler) countCoresFromOIR(pod *api.Pod, cgroupInfo *lifecycle.CgroupInfo) int64 {
	var coresAccu int64
	for _, container := range pod.Spec.Containers {
		coresAccu = coresAccu + e.gatherContainerRequest(container)
	}
	return coresAccu
}

func (e *eventHandler) eventReplyGenerator(error string, cgroupInfo *lifecycle.CgroupInfo, cgropupResource *lifecycle.CgroupResource) *lifecycle.EventReply {
	return &lifecycle.EventReply{
		Error:          error,
		CgroupInfo:     cgroupInfo,
		CgroupResource: cgropupResource,
	}
}

func (e *eventHandler) reserveCores(cores int64) ([]int, error) {
	cpus := e.CPUTopology.GetAvailableCPUs()
	if len(cpus) < int(cores) {
		return nil, fmt.Errorf("cannot reserved requested number of cores")
	}
	var reservedCores []int

	for i := 0; i < int(cores); i++ {
		if err := e.CPUTopology.Reserve(cpus[i]); err != nil {
			return reservedCores, err
		}
		reservedCores = append(reservedCores, cpus[i])
	}
	return reservedCores, nil

}

func (e *eventHandler) reclaimCores(cores []int) {
	for _, core := range cores {
		e.CPUTopology.Reclaim(core)
	}
}

func parseCorse(cores []int) string {
	var coresStr []string
	for _, core := range cores {
		coresStr = append(coresStr, strconv.Itoa(core))
	}
	return strings.Join(coresStr, ",")
}

func (e *eventHandler) preStart(event *lifecycle.Event) (reply *lifecycle.EventReply, err error) {
	glog.Infof("available cores before %v", e.CPUTopology)
	glog.Infof("Received PreStart event: %v\n", event.CgroupInfo)
	pod, err := getPod(event.Pod)
	if err != nil {
		return e.eventReplyGenerator(err.Error(), event.CgroupInfo, nil), err
	}
	oirCores := e.countCoresFromOIR(pod, event.CgroupInfo)
	glog.Infof("Pod %s requested %d cores", pod.Name, oirCores)

	if oirCores == 0 {
		glog.Infof("Pod %q isn't managed by this isolator", pod.Name)
		return e.eventReplyGenerator("", event.CgroupInfo, &lifecycle.CgroupResource{}), nil
	}

	reservedCores, err := e.reserveCores(oirCores)
	if err != nil {
		e.reclaimCores(reservedCores)
		return e.eventReplyGenerator(err.Error(), event.CgroupInfo, nil), err
	}

	cgroupResource := &lifecycle.CgroupResource{
		Value:           parseCorse(reservedCores),
		CgroupSubsystem: lifecycle.CgroupResource_CPUSET_CPUS,
	}
	glog.Infof("cores %v", parseCorse(reservedCores))
	glog.Infof("available cores after %v", e.CPUTopology)

	return e.eventReplyGenerator("", event.CgroupInfo, cgroupResource), nil
}

// TODO: implement PostStop
func (e *eventHandler) Notify(context context.Context, event *lifecycle.Event) (reply *lifecycle.EventReply, err error) {
	switch event.Kind {
	case lifecycle.Event_POD_PRE_START:
		return e.preStart(event)
	default:
		return nil, fmt.Errorf("Wrong event type")
	}

}
