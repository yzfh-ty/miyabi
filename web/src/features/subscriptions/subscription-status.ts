import type { SubscriptionItem } from '@/api/subscriptions'

export function isPendingSubscription(item: SubscriptionItem) {
  return item.status === 'waiting' || item.status === 'stale'
}
