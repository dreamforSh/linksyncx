export default {
  clash: {
    title: 'Clash Subscription Pool',
    description: 'Import Clash subscriptions and assign their nodes to accounts as dedicated egress IPs',
    common: {
      tag: 'Clash'
    },
    actions: {
      addProfile: 'Add subscription',
      settings: 'Pool settings',
      refreshNow: 'Refresh now',
      forceRefresh: 'Force refresh',
      viewNodes: 'View nodes',
      forceDelete: 'Force delete',
      testLatency: 'Test latency',
      probeExit: 'Probe exit',
      enable: 'Enable',
      disable: 'Disable',
      acceptExit: 'Accept',
      preview: 'Dry run',
      clearSelection: 'Clear selection'
    },
    disabledBanner: {
      title: 'Clash pool is disabled',
      description: 'The server runs with clash_pool.mode=disabled. Subscriptions and nodes are read-only and cannot be added, refreshed or tested. Change the configuration and restart the service to enable it.'
    },
    stats: {
      profiles: 'Subscriptions',
      profilesHint: '{count} enabled',
      nodes: 'Available nodes',
      nodesHint: '{total} nodes in total',
      healthy: 'Healthy nodes',
      healthyHint: '{count} unhealthy',
      bound: 'Bound accounts',
      boundHint: 'Up to {max} account(s) per exit',
      boundHintUnknown: 'Accounts using Clash exits'
    },
    runtime: {
      title: 'Core status',
      subtitle: 'The mihomo core exposes one local listener per node; bound accounts egress through it',
      resync: 'Resync',
      resyncing: 'Syncing...',
      resyncSuccess: 'Core configuration resynced',
      resyncFailed: 'Failed to resync the core',
      loadFailed: 'Failed to load core status',
      mode: 'Mode',
      modes: {
        embedded: 'Embedded core',
        external: 'External core',
        disabled: 'Disabled'
      },
      ready: 'Ready',
      notReady: 'Not ready',
      version: 'Core version',
      listeners: 'Listeners',
      listenerFailures: '{count} failed self-check',
      lastApplied: 'Last applied',
      configHash: 'Config hash',
      instances: 'Instances',
      unreadyInstances: '{count} not ready',
      instance: 'Instance',
      status: 'Status',
      heartbeat: 'Heartbeat',
      thisInstance: 'This instance',
      configDiffers: 'Out of sync',
      lastError: 'Last error: {error}'
    },
    interval: {
      manual: 'Manual only',
      everyHours: 'Every {hours}h',
      everyMinutes: 'Every {minutes} min',
      custom: 'Custom'
    },
    formats: {
      clashYaml: 'Clash YAML',
      base64Yaml: 'Base64 encoded',
      uriList: 'URI list'
    },
    health: {
      healthy: 'Healthy',
      unhealthy: 'Unhealthy',
      unknown: 'Unchecked'
    },
    nodeStatus: {
      active: 'Active',
      missing: 'Missing',
      disabled: 'Disabled',
      invalid: 'Invalid'
    },
    platformResult: {
      pass: 'Reachable',
      warn: 'Possibly limited',
      fail: 'Unreachable',
      challenge: 'Challenge required',
      unchecked: 'Unchecked'
    },
    reasons: {
      subscriptionDeleted: 'Subscription deleted',
      subscriptionDisabled: 'Subscription disabled',
      nodeMissing: 'Node removed from the subscription',
      nodeDisabled: 'Node disabled',
      healthFailing: 'Health check failing',
      nodeInvalid: 'Invalid node: {detail}',
      exitChanged: 'Exit IP changed to {ip}, awaiting confirmation',
      disabledByAdmin: 'Disabled by an admin',
      loopbackServer: 'Node server is a loopback or link-local address',
      privateServer: 'Node server is a private network address (can be allowed in the config)',
      exitIsServer: 'Exit IP equals this server\'s own IP; no isolation',
      listenerUnavailable: 'Listener port unavailable: {detail}'
    },
    profiles: {
      title: 'Subscriptions',
      subtitle: '{count} subscription(s)',
      available: 'available',
      healthyCount: '{count} healthy',
      unhealthyCount: '{count} unhealthy',
      missingCount: '{count} missing',
      boundHint: 'Accounts using this subscription\'s nodes (shadows excluded)',
      boundCount: '{count} account(s)',
      expired: 'Expired',
      expiresInDays: 'Expires in {days}d',
      refreshStatus: {
        never: 'Never',
        ok: 'OK',
        error: 'Failed',
        skipped: 'Skipped'
      },
      skippedHint: 'Drop protection kicked in and nodes were not updated. Force a refresh once you have verified the subscription content.',
      neverRefreshed: 'Never refreshed',
      empty: 'No Clash subscriptions yet',
      emptyHint: 'Once a subscription is added, its nodes appear as dedicated exits in the account proxy selector.',
      loadFailed: 'Failed to load subscriptions',
      enabledToast: 'Subscription enabled',
      disabledToast: 'Subscription disabled',
      toggleFailed: 'Failed to update the subscription',
      columns: {
        name: 'Subscription',
        nodes: 'Nodes',
        traffic: 'Traffic',
        expire: 'Expires',
        lastRefresh: 'Last refresh',
        enabled: 'Enabled',
        actions: 'Actions'
      }
    },
    refresh: {
      success: '"{name}" refreshed: {inserted} added, {updated} updated, {missing} missing',
      failed: '"{name}" refresh failed: {error}',
      requestFailed: 'Failed to refresh the subscription',
      forceTitle: 'Node drop protection',
      forceMessage: 'The latest fetch of "{name}" returned far fewer nodes, so the update was skipped to avoid removing nodes by mistake. Force the refresh once you have verified the subscription; vanished nodes will be marked missing.',
      skippedTitle: 'Drop protection kicked in, nodes were not updated',
      failedTitle: 'Fetch failed. Use "Refresh now" in the list to retry later',
      parsed: 'Parsed',
      filtered: 'Filtered',
      private: 'Private',
      inserted: 'Added',
      updated: 'Updated',
      missing: 'Missing'
    },
    toggleDialog: {
      title: 'Disable subscription',
      message: '{count} account(s) use nodes of "{name}". Disabling pauses those accounts until the subscription is enabled again or they are rebound to another proxy.'
    },
    deleteDialog: {
      title: 'Delete subscription',
      message: 'Delete the subscription "{name}"? All of its nodes will stop serving.',
      inUseTitle: 'Subscription in use',
      inUseMessage: 'These accounts still use exits of "{name}":',
      forceHint: 'Force deleting pauses these accounts until a new proxy is assigned in the account editor.',
      success: 'Subscription "{name}" deleted',
      failed: 'Failed to delete the subscription'
    },
    form: {
      createTitle: 'Add Clash subscription',
      editTitle: 'Edit subscription',
      name: 'Name',
      namePlaceholder: 'e.g. Airport A',
      enabled: 'Enabled',
      enabledHint: 'Disabled subscriptions stop serving and pause bound accounts',
      url: 'Subscription URL',
      urlHint: 'Clash YAML, Base64 and URI lists are supported. The URL is stored encrypted and only shown masked afterwards.',
      urlKeepHint: 'Current: {masked}. Leave empty to keep it.',
      userAgent: 'User-Agent',
      userAgentPlaceholder: 'Empty uses the pool default',
      interval: 'Auto refresh',
      customMinutes: 'Interval (minutes)',
      customMinutesHint: '10 to 10080 minutes',
      include: 'Include pattern',
      includePlaceholder: 'Empty means no restriction',
      includeHint: "Matched against node names using Go RE2 syntax, e.g. (?i)(hk{'|'}sg{'|'}jp)",
      exclude: 'Exclude pattern',
      useDefaultExclude: 'Use the default exclude rule',
      excludeHint: 'Matching nodes are skipped; empty excludes nothing',
      excludeDefaultHint: 'Filters info entries such as remaining traffic, expiry date or website',
      fetchProxy: 'Fetch through proxy',
      fetchProxyNone: 'Direct (no proxy)',
      fetchProxyHint: 'Use a manual proxy when the subscription host is unreachable or restricts source IPs',
      notes: 'Notes',
      notesPlaceholder: 'Optional',
      submitCreate: 'Add and fetch',
      creating: 'Fetching subscription...',
      updated: 'Subscription saved',
      saveFailed: 'Failed to save the subscription',
      validation: {
        nameRequired: 'Please enter a name',
        urlRequired: 'Please enter the subscription URL',
        urlInvalid: 'The subscription URL must start with http:// or https://',
        intervalRange: 'The refresh interval must be between {min} and {max} minutes'
      }
    },
    preview: {
      title: 'Dry run',
      hint: 'Fetch and parse the subscription with the current settings without saving anything',
      editHint: 'Re-enter the full subscription URL (the stored URL is never sent back)',
      running: 'Parsing...',
      failed: 'Dry run failed',
      format: 'Format',
      nodeCount: 'Nodes',
      usable: 'Usable',
      excluded: 'Excluded',
      excludedTag: 'Excluded',
      traffic: 'Traffic {used} / {total}',
      expire: 'Expires {date}',
      truncated: 'Showing the first {shown} of {total} nodes',
      skipped: '{count} node(s) could not be parsed',
      columns: {
        name: 'Node',
        type: 'Type',
        server: 'Server'
      }
    },
    createResult: {
      title: 'Subscription added',
      created: 'Subscription "{name}" added',
      notRefreshed: 'The subscription is disabled, so no nodes were fetched yet.',
      refreshOk: 'The first fetch succeeded; nodes are being loaded into the core.',
      refreshSkipped: 'The first fetch triggered the node drop protection.',
      refreshFailed: 'The first fetch failed. The subscription was saved and can be retried later.',
      done: 'Done'
    },
    nodes: {
      title: 'Nodes',
      subtitle: '{count} node(s)',
      searchPlaceholder: 'Search node, server or exit IP',
      filters: {
        profile: 'Subscription',
        status: 'Status',
        health: 'Health',
        bound: 'Binding',
        allProfiles: 'All subscriptions',
        allStatus: 'Any status',
        allHealth: 'All health',
        boundAll: 'All',
        boundOnly: 'Bound',
        unboundOnly: 'Free'
      },
      columns: {
        name: 'Node',
        status: 'Status',
        exit: 'Exit',
        platforms: 'Platforms',
        accounts: 'Accounts',
        listenPort: 'Listener',
        actions: 'Actions'
      },
      selectedCount: '{count} node(s) selected',
      testingLatency: 'Testing...',
      listenPort: 'Listener {port}',
      probingExit: 'Probing...',
      missingSince: 'Removed {time}',
      consecutiveFailures: '{count} failures in a row',
      checkedAt: 'Checked {time}',
      exitUnprobed: 'Not probed',
      pendingExit: 'New exit {ip} awaiting confirmation',
      exitStale: 'Probe failed; exit info may be outdated',
      shadowTag: 'shadow',
      empty: 'No matching nodes',
      emptyHint: 'Adjust the filters or refresh a subscription to fetch nodes.',
      loadFailed: 'Failed to load nodes',
      latencyDone: 'Latency test finished: {success} succeeded, {failed} failed',
      latencyFailed: 'Latency test failed',
      probeDone: 'Exit probe finished: {success} succeeded, {failed} failed',
      probeChanged: 'Exit probe finished: {success} succeeded, {failed} failed; {changed} node(s) changed their exit IP and await confirmation',
      probeFailed: 'Exit probe failed',
      enabledToast: 'Node enabled',
      disabledToast: 'Node disabled',
      toggleFailed: 'Failed to update the node',
      acceptSuccess: 'New exit accepted; bound accounts will resume',
      acceptFailed: 'Failed to accept the exit change',
      disableConfirm: {
        title: 'Disable node',
        message: 'Disabling "{name}" drops its connections immediately.',
        bound: 'These {count} account(s) use this exit and will be paused:'
      },
      acceptConfirm: {
        title: 'Accept exit change',
        message: 'The exit IP of "{name}" changed from {from} to {to}. Accounts using this exit resume once you accept.'
      }
    },
    settings: {
      title: 'Clash pool settings',
      sections: {
        binding: 'Exit binding',
        health: 'Health checks',
        exit: 'Exit probing',
        subscription: 'Subscriptions'
      },
      fields: {
        max_accounts_per_exit: {
          label: 'Accounts per exit',
          hint: 'Maximum accounts sharing one exit IP; 1 means exclusive'
        },
        allow_unprobed_exit_binding: {
          label: 'Allow unprobed exits',
          hint: 'Allow binding nodes whose exit IP is unknown. Exit IP exclusivity cannot be guaranteed then; not recommended'
        },
        exit_change_policy: {
          label: 'When the exit IP changes',
          hint: 'What happens when a node keeps its address but egresses from a new IP'
        },
        health_test_url: {
          label: 'Health check URL',
          hint: 'Requested through each node to measure latency'
        },
        health_timeout_ms: {
          label: 'Check timeout (ms)',
          hint: 'Timeout of a single latency test'
        },
        pause_ttl_minutes: {
          label: 'Pause duration (min)',
          hint: 'Temporary unschedulable window applied to bound accounts, renewed while the node stays down'
        },
        bound_check_interval_seconds: {
          label: 'Bound node interval (s)',
          hint: 'Health check frequency of nodes used by accounts'
        },
        unbound_check_interval_seconds: {
          label: 'Free node interval (s)',
          hint: 'Health check frequency of nodes without accounts'
        },
        failure_threshold: {
          label: 'Failure threshold',
          hint: 'Consecutive failures before a node is marked unhealthy and its accounts are paused'
        },
        recovery_threshold: {
          label: 'Recovery threshold',
          hint: 'Consecutive successes before a node is healthy again'
        },
        exit_probe_interval_minutes: {
          label: 'Exit probe interval (min)',
          hint: 'How often exit IPs are probed again'
        },
        exit_probe_per_minute: {
          label: 'Probes per minute',
          hint: 'Rate limit of exit probes to stay within IP lookup service limits'
        },
        platform_checks_enabled: {
          label: 'Check AI platform reachability',
          hint: 'Also check OpenAI, Anthropic, Gemini and Grok when probing exits'
        },
        default_user_agent: {
          label: 'Default User-Agent',
          hint: 'Used when a subscription does not set its own User-Agent'
        },
        drop_protection_percent: {
          label: 'Drop protection (%)',
          hint: 'Skip a refresh when the node count drops by more than this share; 0 disables it'
        },
        missing_retention_days: {
          label: 'Keep missing nodes (days)',
          hint: 'How long vanished nodes are kept before they are reclaimed'
        }
      },
      exitPolicy: {
        pause: 'Pause bound accounts until an admin accepts',
        accept: 'Accept the new exit automatically'
      },
      range: 'Range {min} - {max}',
      rangeError: 'Enter a whole number between {min} and {max}',
      urlError: 'Enter an address starting with http:// or https://',
      saved: 'Settings saved',
      saveFailed: 'Failed to save settings',
      loadFailed: 'Failed to load settings'
    },
    selector: {
      searchPlaceholder: 'Search proxies, nodes, exit IPs or countries',
      idle: 'Free',
      usedBy: 'Used by {names}',
      usedByMany: 'Used by {names} and others ({count})',
      full: 'Full (up to {max} account(s) per exit)',
      exitUnprobed: 'Exit not probed',
      exitUnprobedBlocked: 'Exit IP not probed yet, cannot bind',
      platformWarning: 'This platform may be unreachable through this exit',
      selectedLabel: '{node} ({ip})',
      unlistedProxy: 'Proxy #{id} (not in the list)'
    },
    errors: {
      exitOccupied: 'This Clash exit is already used by other accounts and has reached the per-exit limit',
      exitOccupiedWithAccounts: 'This Clash exit is already used by {accounts} and has reached the per-exit limit',
      exitUnavailable: 'This Clash exit is unavailable (node down, disabled or exit change pending). Pick another exit',
      exitUnprobed: 'The exit IP of this Clash exit has not been probed yet. Probe it on the Clash page first',
      relayUnsupported: 'Accounts using a custom relay base URL cannot use a Clash exit',
      bulkUnsupported: 'Clash exits are assigned one account at a time from the account editor, not in bulk',
      managedReadonly: 'This proxy is managed by a Clash subscription. Manage it on the Clash page',
      portReserved: 'This host and port range is reserved for Clash exit listeners and cannot be used by manual proxies',
      encryptionKeyRequired: 'Saving subscription URLs requires a fixed encryption key: configure TOTP_ENCRYPTION_KEY on the server and restart',
      profileInUse: 'Accounts still use exits of this subscription',
      profileInUseWithAccounts: 'Accounts still use exits of this subscription: {accounts}',
      profileInvalid: 'Invalid subscription settings: {message}',
      profileNotFound: 'The subscription does not exist or was deleted',
      nodeNotFound: 'The node does not exist or was deleted',
      fetchFailed: 'Failed to fetch the subscription: {message}',
      poolDisabled: 'The Clash pool is disabled (clash_pool.mode=disabled)',
      runtimeUnavailable: 'The Clash core is not ready. Check the core status and retry',
      tooManyNodes: 'Select at most 50 nodes at a time',
      batchCreate: 'A Clash exit can only be bound to one account. Use a manual proxy or no proxy for batch creation, then assign Clash exits one by one in the account editor'
    }
  }
}
