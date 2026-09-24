import { inject } from 'vue'
import { routeLocationKey, routerKey } from 'vue-router'

/**
 * 读取当前路由与路由器；没有安装路由时返回 null。
 * 管理端视图在单测里常以无路由方式挂载，带默认值的 inject 不会产生注入缺失告警。
 */
export function useOptionalRouter() {
  return {
    route: inject(routeLocationKey, null),
    router: inject(routerKey, null)
  }
}
