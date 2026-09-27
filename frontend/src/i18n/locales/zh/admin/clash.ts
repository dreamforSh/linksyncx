export default {
  clash: {
    title: 'Clash 订阅代理池',
    description: '导入 Clash 订阅，把节点作为独立出口 IP 分配给账号',
    common: {
      tag: 'Clash'
    },
    actions: {
      addProfile: '添加订阅',
      settings: '池设置',
      refreshNow: '立即刷新',
      forceRefresh: '强制刷新',
      viewNodes: '查看节点',
      forceDelete: '强制删除',
      testLatency: '测延迟',
      probeExit: '探测出口',
      enable: '启用',
      disable: '禁用',
      acceptExit: '确认',
      preview: '试解析',
      clearSelection: '清除选择',
      fullCheck: '完整检测',
      batchTest: '批量测试',
      stop: '停止',
      unbind: '解绑'
    },
    disabledBanner: {
      title: 'Clash 代理池未启用',
      description: '服务端配置为 clash_pool.mode=disabled，订阅与节点只能查看，无法新增、刷新或测试。修改配置并重启服务后生效。'
    },
    stats: {
      profiles: '订阅',
      profilesHint: '{count} 个已启用',
      nodes: '可用节点',
      nodesHint: '共 {total} 个节点',
      healthy: '健康节点',
      healthyHint: '{count} 个异常',
      bound: '已绑定账号',
      boundHint: '每个出口最多 {max} 个账号',
      boundHintUnknown: '使用 Clash 出口的账号数',
      trafficToday: '今日流量',
      trafficTotalHint: '累计 {total}'
    },
    runtime: {
      title: '内核状态',
      subtitle: 'mihomo 内核为每个节点提供本地监听端口，账号经它出站',
      resync: '重新同步',
      resyncing: '同步中...',
      resyncSuccess: '已重新同步内核配置',
      resyncFailed: '重新同步失败',
      loadFailed: '加载内核状态失败',
      mode: '运行模式',
      modes: {
        embedded: '内嵌内核',
        external: '外部内核',
        disabled: '已关闭'
      },
      ready: '就绪',
      notReady: '未就绪',
      version: '内核版本',
      listeners: '监听端口',
      listenerFailures: '{count} 个自检失败',
      lastApplied: '最后应用',
      configHash: '配置哈希',
      instances: '实例',
      unreadyInstances: '{count} 个未就绪',
      instance: '实例',
      status: '状态',
      heartbeat: '心跳',
      thisInstance: '本实例',
      configDiffers: '配置未同步',
      lastError: '最后错误：{error}'
    },
    interval: {
      manual: '仅手动',
      everyHours: '每 {hours} 小时',
      everyMinutes: '每 {minutes} 分钟',
      custom: '自定义'
    },
    formats: {
      clashYaml: 'Clash YAML',
      base64Yaml: 'Base64 编码',
      uriList: 'URI 列表'
    },
    health: {
      healthy: '健康',
      unhealthy: '异常',
      unknown: '未检测'
    },
    nodeStatus: {
      active: '正常',
      missing: '已失效',
      disabled: '已禁用',
      invalid: '无效'
    },
    platformResult: {
      pass: '可用',
      warn: '可能受限',
      fail: '不可用',
      challenge: '需人机验证',
      unchecked: '未检测'
    },
    reasons: {
      subscriptionDeleted: '订阅已删除',
      subscriptionDisabled: '订阅已停用',
      nodeMissing: '节点已从订阅中移除',
      nodeDisabled: '节点已禁用',
      healthFailing: '健康检查失败',
      nodeInvalid: '节点配置无效：{detail}',
      exitChanged: '出口 IP 已变为 {ip}，待确认',
      disabledByAdmin: '管理员已禁用',
      loopbackServer: '节点地址为本机或链路本地地址',
      privateServer: '节点地址为内网地址（可在配置中放行）',
      exitIsServer: '出口 IP 与本服务器出口相同，无法隔离',
      listenerUnavailable: '监听端口不可用：{detail}'
    },
    profiles: {
      title: '订阅',
      subtitle: '共 {count} 个订阅',
      available: '可用',
      healthyCount: '{count} 健康',
      unhealthyCount: '{count} 异常',
      missingCount: '{count} 失效',
      boundHint: '使用该订阅节点的账号数（不含影子账号）',
      boundCount: '{count} 个账号',
      measuredValue: '本地 今日 {today} · 累计 {total}',
      measuredHint: '经本服务各节点实际转发的流量（按连接采样，为近似值）；上方进度条为机场上报的用量',
      expired: '已过期',
      expiresInDays: '{days} 天后到期',
      refreshStatus: {
        never: '未刷新',
        ok: '成功',
        error: '失败',
        skipped: '已跳过'
      },
      skippedHint: '节点数骤降保护已生效，本次未更新节点。确认订阅内容无误后可强制刷新。',
      neverRefreshed: '从未刷新',
      empty: '还没有 Clash 订阅',
      emptyHint: '添加订阅后，节点会作为独立出口出现在账号编辑的代理选择器中。',
      loadFailed: '加载订阅失败',
      enabledToast: '订阅已启用',
      disabledToast: '订阅已停用',
      toggleFailed: '更新订阅状态失败',
      columns: {
        name: '订阅',
        nodes: '节点',
        traffic: '流量',
        expire: '到期',
        lastRefresh: '最后刷新',
        enabled: '启用',
        actions: '操作'
      }
    },
    refresh: {
      success: '「{name}」刷新完成：新增 {inserted}，更新 {updated}，失效 {missing}',
      failed: '「{name}」刷新失败：{error}',
      requestFailed: '刷新订阅失败',
      forceTitle: '节点数骤降保护',
      forceMessage: '「{name}」本次拉取到的节点数大幅减少，已跳过更新以免误删节点。确认订阅内容无误后可强制刷新，消失的节点会被标记为失效。',
      skippedTitle: '已触发节点数骤降保护，本次未更新节点',
      failedTitle: '拉取失败，可稍后在列表中点“立即刷新”重试',
      parsed: '解析',
      filtered: '过滤',
      private: '内网地址',
      inserted: '新增',
      updated: '更新',
      missing: '失效'
    },
    toggleDialog: {
      title: '停用订阅',
      message: '「{name}」的节点正被 {count} 个账号使用。停用后这些账号会被暂停调度，直到订阅重新启用或改绑其他代理。'
    },
    deleteDialog: {
      title: '删除订阅',
      message: '确定删除订阅「{name}」吗？其所有节点将停止服务。',
      inUseTitle: '订阅仍在使用中',
      inUseMessage: '以下账号仍在使用「{name}」的出口：',
      forceHint: '强制删除后，这些账号会被暂停调度，直到在账号编辑中重新指定代理。',
      success: '订阅「{name}」已删除',
      failed: '删除订阅失败'
    },
    form: {
      createTitle: '添加 Clash 订阅',
      editTitle: '编辑订阅',
      name: '名称',
      namePlaceholder: '例如：机场 A',
      enabled: '启用订阅',
      enabledHint: '停用后节点停止服务，绑定账号会被暂停',
      url: '订阅链接',
      urlHint: '支持 Clash YAML、Base64 与 URI 列表格式；链接加密保存，之后只显示脱敏形式。',
      urlKeepHint: '当前：{masked}。留空表示不修改。',
      userAgent: 'User-Agent',
      userAgentPlaceholder: '留空使用池设置中的默认值',
      interval: '自动刷新',
      customMinutes: '间隔（分钟）',
      customMinutesHint: '10 到 10080 分钟',
      include: '包含正则',
      includePlaceholder: '留空表示不限制',
      includeHint: "按节点名匹配，使用 Go RE2 语法，例如 (?i)(hk{'|'}sg{'|'}jp)",
      exclude: '排除正则',
      useDefaultExclude: '使用默认排除规则',
      excludeHint: '命中的节点会被跳过；留空表示不排除任何节点',
      excludeDefaultHint: '默认过滤“剩余流量 / 到期时间 / 官网”等信息节点',
      fetchProxy: '拉取订阅使用的代理',
      fetchProxyNone: '直连（不使用代理）',
      fetchProxyHint: '订阅地址无法直连或限制来源 IP 时，可通过手动代理拉取',
      notes: '备注',
      notesPlaceholder: '可选',
      submitCreate: '添加并拉取',
      creating: '正在拉取订阅...',
      updated: '订阅已保存',
      saveFailed: '保存订阅失败',
      validation: {
        nameRequired: '请输入订阅名称',
        urlRequired: '请输入订阅链接',
        urlInvalid: '请输入以 http:// 或 https:// 开头的订阅链接',
        intervalRange: '自动刷新间隔需在 {min} 到 {max} 分钟之间'
      }
    },
    preview: {
      title: '试解析',
      hint: '按当前配置拉取并解析订阅，不会保存任何内容',
      editHint: '需要重新输入完整的订阅链接（已保存的链接不会回显）',
      running: '解析中...',
      failed: '试解析失败',
      format: '格式',
      nodeCount: '节点数',
      usable: '可用',
      excluded: '已排除',
      excludedTag: '已排除',
      traffic: '流量 {used} / {total}',
      expire: '到期 {date}',
      truncated: '仅显示前 {shown} 个节点，共 {total} 个',
      skipped: '{count} 个节点无法解析',
      columns: {
        name: '节点',
        type: '类型',
        server: '服务器'
      }
    },
    createResult: {
      title: '订阅已添加',
      created: '订阅「{name}」已添加',
      notRefreshed: '订阅未启用，暂未拉取节点。',
      refreshOk: '已完成首次拉取，节点正在接入内核。',
      refreshSkipped: '首次拉取触发了节点数骤降保护。',
      refreshFailed: '首次拉取失败，订阅已保存，可稍后重试。',
      done: '完成'
    },
    nodes: {
      title: '节点',
      subtitle: '共 {count} 个节点',
      searchPlaceholder: '搜索节点名、服务器或出口 IP',
      filters: {
        profile: '订阅',
        status: '状态',
        health: '健康',
        bound: '绑定',
        allProfiles: '全部订阅',
        allStatus: '全部状态',
        allHealth: '全部健康',
        boundAll: '全部',
        boundOnly: '已绑定',
        unboundOnly: '空闲'
      },
      columns: {
        name: '节点',
        status: '状态',
        exit: '出口',
        traffic: '流量',
        platforms: '平台可达性',
        accounts: '绑定账号',
        listenPort: '监听端口',
        actions: '操作'
      },
      view: {
        label: '显示方式',
        card: '卡片',
        table: '列表'
      },
      sort: {
        label: '排序',
        default: '默认排序',
        latency: '延迟最低',
        trafficToday: '今日流量最多',
        trafficTotal: '累计流量最多',
        name: '按名称'
      },
      traffic: {
        today: '今日',
        live: '实时',
        trend: '近 7 天',
        totalValue: '累计 {value}',
        noTraffic: '近 7 天无流量',
        trendAria: '近 7 天流量趋势',
        split: '上行 {up} · 下行 {down}',
        rateSplit: '上行 {up} · 下行 {down}',
        connections: '{count} 个连接',
        idle: '当前没有经过此节点的连接',
        updatedAt: '统计更新于 {time}',
        approxHint: '按连接采样统计：连接关闭前最后几秒的流量可能未计入，为近似值'
      },
      card: {
        select: '选择 {name}',
        latency: '延迟',
        idle: '空闲',
        manageBindings: '管理绑定账号'
      },
      selectPage: '全选本页',
      batch: {
        scopeFiltered: '测试当前筛选条件下的全部可用节点（所有页）',
        latencyHint: '经内核测量延迟，更新健康状态',
        exitHint: '探测出口 IP 与 AI 平台可达性',
        fullHint: '先测延迟，通过的节点再探测出口',
        success: '成功 {count}',
        failed: '失败 {count}',
        changed: '出口变化 {count}',
        skipped: '跳过 {count}',
        stopped: '已停止：完成 {done} / {total} 个节点',
        empty: '没有可测试的节点（只测试订阅已启用、状态正常的节点）',
        loadIdsFailed: '获取节点列表失败',
        fullDone: '完整检测完成：成功 {success}，失败 {failed}',
        fullChanged: '完整检测完成：成功 {success}，失败 {failed}；{changed} 个节点出口 IP 发生变化，待确认'
      },
      bindings: {
        title: '绑定账号 · {name}',
        occupancy: '出口占用 {used} / {max}',
        otherNodes: '其中 {count} 个经同一出口 IP 的其他节点',
        current: '当前绑定',
        none: '暂无账号使用此节点',
        followsParent: '跟随母账号',
        unbindConfirm: '解绑后「{name}」不再使用任何代理，请求会从本机直连上游。',
        pauseOnUnbind: '同时停止调度该账号（推荐），重新指定出口后再启用',
        confirmUnbind: '确认解绑',
        add: '添加账号',
        remaining: '还可绑定 {count} 个',
        full: '已达单出口账号上限：先解绑，或在池设置中调高上限',
        searchPlaceholder: '搜索账号名称',
        noResults: '没有匹配的账号',
        platformWarning: '此出口访问该账号所属平台的检测未通过',
        prevPage: '上一页',
        nextPage: '下一页',
        unavailable: '节点当前不可用，无法绑定：{reason}',
        unprobed: '出口 IP 尚未探测，暂不能绑定；请先探测出口',
        shadowHint: '影子账号跟随母账号的代理，不能单独绑定',
        relayHint: '启用了自定义中转地址，不能使用 Clash 出口',
        currentNone: '当前未使用代理',
        currentHere: '已绑定此节点',
        currentClash: '当前出口：{name}（将改绑到此节点）',
        currentManual: '当前代理：{name}（将改绑到此节点）',
        loadFailed: '加载账号失败',
        bindFailed: '绑定失败',
        bound: '已绑定 {count} 个账号',
        partial: '已绑定 {success} 个，{failed} 个失败：{error}',
        binding: '绑定中...',
        submit: '绑定所选（{count}）',
        unbound: '已解绑「{name}」',
        unboundPaused: '已解绑「{name}」并停止调度',
        unbindFailed: '解绑失败'
      },
      selectedCount: '已选 {count} 个节点',
      testingLatency: '测试中...',
      listenPort: '监听 {port}',
      probingExit: '探测中...',
      missingSince: '{time}失效',
      consecutiveFailures: '连续失败 {count} 次',
      checkedAt: '检测于 {time}',
      exitUnprobed: '未探测',
      pendingExit: '新出口 {ip} 待确认',
      exitStale: '探测失败，出口信息可能已过期',
      shadowTag: '影子',
      empty: '没有匹配的节点',
      emptyHint: '调整筛选条件，或刷新订阅以拉取节点。',
      loadFailed: '加载节点失败',
      latencyDone: '延迟测试完成：成功 {success}，失败 {failed}',
      latencyFailed: '延迟测试失败',
      probeDone: '出口探测完成：成功 {success}，失败 {failed}',
      probeChanged: '出口探测完成：成功 {success}，失败 {failed}；{changed} 个节点出口 IP 发生变化，待确认',
      probeFailed: '出口探测失败',
      enabledToast: '节点已启用',
      disabledToast: '节点已禁用',
      toggleFailed: '更新节点状态失败',
      acceptSuccess: '已确认新出口，绑定账号将恢复调度',
      acceptFailed: '确认出口变更失败',
      disableConfirm: {
        title: '禁用节点',
        message: '禁用「{name}」会立即断开经由它的连接。',
        bound: '以下 {count} 个账号正在使用该出口，禁用后会被暂停调度：'
      },
      acceptConfirm: {
        title: '确认出口变更',
        message: '节点「{name}」的出口 IP 已由 {from} 变为 {to}。确认后，使用该出口的账号将恢复调度。'
      }
    },
    settings: {
      title: 'Clash 代理池设置',
      sections: {
        binding: '出口绑定',
        health: '健康检查',
        exit: '出口探测',
        subscription: '订阅'
      },
      fields: {
        max_accounts_per_exit: {
          label: '单出口账号上限',
          hint: '同一出口 IP 最多可绑定的账号数，1 表示独占'
        },
        allow_unprobed_exit_binding: {
          label: '允许绑定未探测出口',
          hint: '出口 IP 未知的节点也可以被绑定；此时无法保证出口 IP 独占，不建议开启'
        },
        exit_change_policy: {
          label: '出口 IP 变化时',
          hint: '节点出口 IP 原地变化后的处理方式'
        },
        health_test_url: {
          label: '健康检查 URL',
          hint: '经节点请求该地址测量延迟'
        },
        health_timeout_ms: {
          label: '检查超时（毫秒）',
          hint: '单次延迟测试的超时时间'
        },
        pause_ttl_minutes: {
          label: '暂停时长（分钟）',
          hint: '节点失效时施加给绑定账号的临时不可调度时长，失效期间自动续期'
        },
        bound_check_interval_seconds: {
          label: '已绑定节点检查间隔（秒）',
          hint: '有账号使用的节点的健康检查频率'
        },
        unbound_check_interval_seconds: {
          label: '空闲节点检查间隔（秒）',
          hint: '无账号使用的节点的健康检查频率'
        },
        failure_threshold: {
          label: '失败阈值',
          hint: '连续失败达到该次数后标记为异常，并暂停绑定账号'
        },
        recovery_threshold: {
          label: '恢复阈值',
          hint: '连续成功达到该次数后恢复健康'
        },
        exit_probe_interval_minutes: {
          label: '出口探测间隔（分钟）',
          hint: '定期重新探测节点出口 IP 的周期'
        },
        exit_probe_per_minute: {
          label: '每分钟探测上限',
          hint: '限制出口探测速率，避免触发 IP 查询服务限流'
        },
        platform_checks_enabled: {
          label: '检测 AI 平台可达性',
          hint: '探测出口时顺带检测 OpenAI、Anthropic、Gemini、Grok 能否访问'
        },
        default_user_agent: {
          label: '默认 User-Agent',
          hint: '订阅未单独指定 User-Agent 时使用'
        },
        drop_protection_percent: {
          label: '节点骤降保护（%）',
          hint: '单次刷新节点数下降超过该比例时跳过更新，0 表示关闭'
        },
        missing_retention_days: {
          label: '失效节点保留（天）',
          hint: '从订阅中消失的节点保留多久后回收'
        }
      },
      exitPolicy: {
        pause: '暂停绑定账号，等待管理员确认',
        accept: '自动接受新出口'
      },
      range: '范围 {min} - {max}',
      rangeError: '请输入 {min} 到 {max} 之间的整数',
      urlError: '请输入 http:// 或 https:// 开头的地址',
      saved: '设置已保存',
      saveFailed: '保存设置失败',
      loadFailed: '加载设置失败'
    },
    selector: {
      searchPlaceholder: '搜索代理、节点、出口 IP 或国家',
      idle: '空闲',
      usedBy: '已被 {names} 使用',
      usedByMany: '已被 {names} 等 {count} 个账号使用',
      full: '已达上限（每个出口最多 {max} 个账号）',
      exitUnprobed: '出口未探测',
      exitUnprobedBlocked: '出口 IP 未探测，暂不可绑定',
      platformWarning: '该平台在此出口可能不可用',
      selectedLabel: '{node}（{ip}）',
      unlistedProxy: '代理 #{id}（不在可选列表中）'
    },
    errors: {
      exitOccupied: '该 Clash 出口已被其他账号占用，达到单出口账号上限',
      exitOccupiedWithAccounts: '该 Clash 出口已被 {accounts} 占用，达到单出口账号上限',
      exitUnavailable: '该 Clash 出口当前不可用（节点失效、被禁用或出口变更待确认），请选择其他出口',
      exitUnprobed: '该 Clash 出口尚未探测出口 IP，请先在 Clash 订阅页探测出口后再绑定',
      relayUnsupported: '启用了自定义中转地址（Base URL）的账号不能使用 Clash 出口',
      bulkUnsupported: '批量编辑不能设置 Clash 出口：请在账号编辑中逐个指定，或在 Clash 订阅页的节点上绑定账号',
      managedReadonly: '该代理由 Clash 订阅托管，请在 Clash 订阅页管理',
      portReserved: '该地址与端口段保留给 Clash 出口监听，手动代理不能使用',
      encryptionKeyRequired: '保存订阅链接需要固定的加密密钥：请在服务端配置 TOTP_ENCRYPTION_KEY 并重启后重试',
      profileInUse: '仍有账号在使用该订阅的出口',
      profileInUseWithAccounts: '仍有账号在使用该订阅的出口：{accounts}',
      profileInvalid: '订阅配置无效：{message}',
      profileNotFound: '订阅不存在或已被删除',
      nodeNotFound: '节点不存在或已被删除',
      fetchFailed: '拉取订阅失败：{message}',
      poolDisabled: 'Clash 代理池未启用（clash_pool.mode=disabled），无法执行此操作',
      runtimeUnavailable: 'Clash 内核未就绪，请检查内核状态后重试',
      tooManyNodes: '单次最多可选择 50 个节点',
      batchCreate: '一个 Clash 出口只能绑定一个账号：批量创建时请改用手动代理或不使用代理，创建后再在账号编辑中逐个指定 Clash 出口'
    }
  }
}
