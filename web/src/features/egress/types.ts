import type { EgressPolicy, ID } from '../../domain'
export interface EgressPolicyDrawerProps { show: boolean; terminalNodeId: ID; routeId?: ID; draftId?: ID; policyId?: ID; readonly?: boolean }
export interface EgressPolicyDrawerEmits { 'update:show': [value: boolean]; save: [policy: EgressPolicy]; cancel: [] }
export interface CheckEgressDrawerProps { show: boolean; routeId: ID; subscriptionId?: ID; entryId?: ID; targetId?: ID; scope?: 'route' | 'target_only' }
export interface CheckEgressDrawerEmits { 'update:show': [value: boolean]; cancel: []; completed: [checkIds: ID[]] }
