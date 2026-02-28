package container

// RuntimeResource 表示容器实例运行时信息。
type RuntimeResource struct {
	Name     string
	Type     ContainerType
	Image    string
	Host     string
	Ports    map[string]int
	URI      string
	Metadata map[string]string
}

// RuntimeView 提供运行时资源只读视图。
type RuntimeView interface {
	Get(name string) (RuntimeResource, bool)
}

type runtimeView struct {
	resources map[string]RuntimeResource
}

func newRuntimeView(resources map[string]RuntimeResource) RuntimeView {
	// 关键决策：RuntimeView 构造时立即拷贝，避免 Init 过程中的并发读写风险。
	cp := make(map[string]RuntimeResource, len(resources))
	for k, v := range resources {
		cp[k] = v
	}
	return &runtimeView{resources: cp}
}

func (v *runtimeView) Get(name string) (RuntimeResource, bool) {
	r, ok := v.resources[name]
	return r, ok
}
