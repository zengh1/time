// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package rate provides a rate limiter.
package rate

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// Limit defines the maximum frequency of some events.
// Limit is represented as number of events per second.
// A zero Limit allows no events.
//
// Limit 是每秒的最大事件数，zero Limit 不允许任何事件
type Limit float64

// Inf is the infinite rate limit; it allows all events (even if burst is zero).
// Inf 表示无速率限制，它允许所有事件，即使 burst 字段为 0
const Inf = Limit(math.MaxFloat64)

// Every converts a minimum time interval between events to a Limit.
// Every interval 用于指定事件之间的最小事件间隔，而 Every 会将 interval 转换为一个 Limit
// 例如：每 5 秒一个事件（interval = 5），
// 可以转化为 1 / 5 = 0.2 = 每秒最大 0.2 个 事件（Limit = 0.2）
func Every(interval time.Duration) Limit {
	if interval <= 0 {
		return Inf
	}
	return 1 / Limit(interval.Seconds())
}

// A Limiter controls how frequently events are allowed to happen.
// It implements a "token bucket" of size b, initially full and refilled
// at rate r tokens per second.
// Informally, in any large enough time interval, the Limiter limits the
// rate to r tokens per second, with a maximum burst size of b events.
// As a special case, if r == Inf (the infinite rate), b is ignored.
// See https://en.wikipedia.org/wiki/Token_bucket for more about token buckets.
//
// The zero value is a valid Limiter, but it will reject all events.
// Use NewLimiter to create non-zero Limiters.
//
// Limiter has three main methods, Allow, Reserve, and Wait.
// Most callers should use Wait.
//
// Each of the three methods consumes a single token.
// They differ in their behavior when no token is available.
// If no token is available, Allow returns false.
// If no token is available, Reserve returns a reservation for a future token
// and the amount of time the caller must wait before using it.
// If no token is available, Wait blocks until one can be obtained
// or its associated context.Context is canceled.
//
// The methods AllowN, ReserveN, and WaitN consume n tokens.
//
//
// Limiter 控制允许事件发生的频率。它实现了一个大小为 b 的“令牌桶”，
// 初始为满并以每秒 r 个令牌的速率填充。
// 非正式地，在任何足够大的时间间隔内，限制器将速率限制为每秒 r 个令牌，
// 最大突发大小为 b 个事件。作为一种特殊情况，如果 r == Inf（无限速率），
// 则忽略 b。有关令牌桶的更多信息，请参阅 https:en.wikipedia.orgwikiToken_bucket。
//
// 零值是一个有效的限制器，但它会拒绝所有事件。使用 NewLimiter 创建非零限制器。
//
// Limiter 有三种主要方法，Allow, Reserve, 和 Wait。多数情况应当调用 Wait。
//
// 这三种方法中的每一种都消耗一个令牌。当没有令牌可用时，它们的行为有所不同。如果没有可用的令牌，
// 则 Allow 返回 false。如果没有可用的令牌，Reserve 返回对未来令牌的保留以及调用者在使用它之
// 前必须等待的时间。如果没有可用的令牌，Wait 会阻塞，直到可以获得一个令牌或者其
// 关联的 context.Context 被取消。
//
// AllowN、ReserveN 和 WaitN 方法消耗 n 个令牌。
type Limiter struct {
	mu    sync.Mutex
	limit Limit // 每秒的最大事件数
	// 令牌桶中可以存放令牌的最大数量， 如果 burst 为 0 ，
	// 则除非 limit == Inf，否则不允许处理任何事件
	burst  int
	tokens float64 // 令牌桶中可用的令牌数量
	// last is the last time the limiter's tokens field was updated
	// 记录上次 tokens 更新的时间
	last time.Time
	// lastEvent is the latest time of a rate-limited event (past or future)
	// 记录速率受限制(桶中没有令牌)的时间点，该时间点可能是过去的，
	// 也可能是将来的( Reservation 预定的结束时间点)
	lastEvent time.Time
}

// Limit returns the maximum overall event rate.
func (lim *Limiter) Limit() Limit {
	lim.mu.Lock()
	defer lim.mu.Unlock()
	return lim.limit
}

// Burst returns the maximum burst size. Burst is the maximum number of tokens
// that can be consumed in a single call to Allow, Reserve, or Wait, so higher
// Burst values allow more events to happen at once.
// A zero Burst allows no events, unless limit == Inf.
func (lim *Limiter) Burst() int {
	lim.mu.Lock()
	defer lim.mu.Unlock()
	return lim.burst
}

// NewLimiter returns a new Limiter that allows events up to rate r and permits
// bursts of at most b tokens.
//
// NewLimiter 每秒最多允许 r 个事件，令牌桶容量为 b
func NewLimiter(r Limit, b int) *Limiter {
	return &Limiter{
		limit: r,
		burst: b,
	}
}

// Allow is shorthand for AllowN(time.Now(), 1).
//
// Allow 会从令牌桶中获取 1 个令牌
func (lim *Limiter) Allow() bool {
	return lim.AllowN(time.Now(), 1)
}

// AllowN reports whether n events may happen at time now.
// Use this method if you intend to drop / skip events that exceed the rate limit.
// Otherwise use Reserve or Wait.
//
// AllowN 从令牌桶中获取 n 个令牌，成功获取到则返回 true
// 如果此时已达到速率限制，那么之后到来的事件会被删除 or 跳过，
// 如果不想这么处理，可以调用 Reserve 或者 Wait
func (lim *Limiter) AllowN(now time.Time, n int) bool {
	// 调用 reserveN 来预约令牌，reserveN 的 ok 字段表示
	// 到截至时间（now）是否可以获取足够（n）的令牌，第三个参数
	// maxFutureReserve 表示如果当前不能提供所需数量的令牌，那么你愿意等待多长的时间，
	// 这里传入 0 表示不进行任何等待，
	return lim.reserveN(now, n, 0).ok
}

// A Reservation holds information about events that are permitted by a Limiter to happen after a delay.
// A Reservation may be canceled, which may enable the Limiter to permit additional events.
//
// Reservation 用来保存预定令牌的相关信息
type Reservation struct {
	ok        bool      // 到截至时间是否可以获取足够的令牌
	lim       *Limiter  //
	tokens    int       // 需要获取的令牌数量
	timeToAct time.Time // 需要等待的时间点，到此时才能产生预约所需数量的令牌
	// This is the Limit at reservation time, it can change later.
	limit Limit
}

// OK returns whether the limiter can provide the requested number of tokens
// within the maximum wait time.  If OK is false, Delay returns InfDuration, and
// Cancel does nothing.
func (r *Reservation) OK() bool {
	return r.ok
}

// Delay is shorthand for DelayFrom(time.Now()).
func (r *Reservation) Delay() time.Duration {
	return r.DelayFrom(time.Now())
}

// InfDuration is the duration returned by Delay when a Reservation is not OK.
const InfDuration = time.Duration(1<<63 - 1)

// DelayFrom returns the duration for which the reservation holder must wait
// before taking the reserved action.  Zero duration means act immediately.
// InfDuration means the limiter cannot grant the tokens requested in this
// Reservation within the maximum wait time.
//
// DelayFrom 返回从现在开始，到令牌准备好，一共需要多长时间
// 如果返回 InfDuration，则表示无法准备好令牌
func (r *Reservation) DelayFrom(now time.Time) time.Duration {
	if !r.ok {
		// InfDuration 表示 limiter 无法在最长等待时间内授予此 Reservation 中请求的令牌。
		return InfDuration
	}
	delay := r.timeToAct.Sub(now)
	if delay < 0 {
		return 0
	}
	return delay
}

// Cancel is shorthand for CancelAt(time.Now()).
func (r *Reservation) Cancel() {
	r.CancelAt(time.Now())
}

// CancelAt indicates that the reservation holder will not perform the reserved action
// and reverses the effects of this Reservation on the rate limit as much as possible,
// considering that other reservations may have already been made.
func (r *Reservation) CancelAt(now time.Time) {
	if !r.ok {
		return
	}

	r.lim.mu.Lock()
	defer r.lim.mu.Unlock()

	if r.lim.limit == Inf || r.tokens == 0 || r.timeToAct.Before(now) {
		return
	}

	// calculate tokens to restore
	// The duration between lim.lastEvent and r.timeToAct tells us how many tokens were reserved
	// after r was obtained. These tokens should not be restored.
	restoreTokens := float64(r.tokens) - r.limit.tokensFromDuration(r.lim.lastEvent.Sub(r.timeToAct))
	if restoreTokens <= 0 {
		return
	}
	// advance time to now
	now, _, tokens := r.lim.advance(now)
	// calculate new number of tokens
	tokens += restoreTokens
	if burst := float64(r.lim.burst); tokens > burst {
		tokens = burst
	}
	// update state
	r.lim.last = now
	r.lim.tokens = tokens
	if r.timeToAct == r.lim.lastEvent {
		prevEvent := r.timeToAct.Add(r.limit.durationFromTokens(float64(-r.tokens)))
		if !prevEvent.Before(now) {
			r.lim.lastEvent = prevEvent
		}
	}
}

// Reserve is shorthand for ReserveN(time.Now(), 1).
//
// 预约 1 个令牌
func (lim *Limiter) Reserve() *Reservation {
	return lim.ReserveN(time.Now(), 1)
}

// ReserveN returns a Reservation that indicates how long the caller must wait before n events happen.
// The Limiter takes this Reservation into account when allowing future events.
// The returned Reservation’s OK() method returns false if n exceeds the Limiter's burst size.
// Usage example:
//   r := lim.ReserveN(time.Now(), 1)
//   if !r.OK() {
//     // Not allowed to act! Did you remember to set lim.burst to be > 0 ?
//     return
//   }
//   time.Sleep(r.Delay())
//   Act()
// Use this method if you wish to wait and slow down in accordance with the rate limit without dropping events.
// If you need to respect a deadline or cancel the delay, use Wait instead.
// To drop or skip events exceeding rate limit, use Allow instead.
//
// ReserveN 返回一个 Reservation，指示调用者在 n 个事件发生之前必须等待多长时间。
// Limiter在允许未来事件时会考虑此保留。(没看懂)
// 如果 n 超过限制器的桶容量大小，则返回的 Reservation 的 OK() 方法返回 false
func (lim *Limiter) ReserveN(now time.Time, n int) *Reservation {
	r := lim.reserveN(now, n, InfDuration)
	return &r
}

// Wait is shorthand for WaitN(ctx, 1).
func (lim *Limiter) Wait(ctx context.Context) (err error) {
	return lim.WaitN(ctx, 1)
}

// WaitN blocks until lim permits n events to happen.
// It returns an error if n exceeds the Limiter's burst size, the Context is
// canceled, or the expected wait time exceeds the Context's Deadline.
// The burst limit is ignored if the rate limit is Inf.
//
// WaitN 会阻塞，直到 lim 允许 n 个事件发生。如果 n 超过令牌桶容量，或者 context 被取消，
// 或者预期的等待时间超过了 context.Deadline，它会返回一个错误。如果速率限制为 Inf，则忽略桶容量限制。
func (lim *Limiter) WaitN(ctx context.Context, n int) (err error) {
	lim.mu.Lock()
	burst := lim.burst
	limit := lim.limit
	lim.mu.Unlock()

	// 如果 n 大于令牌桶的最大容量，则返回 error
	if n > burst && limit != Inf {
		return fmt.Errorf("rate: Wait(n=%d) exceeds limiter's burst %d", n, burst)
	}

	// Check if ctx is already cancelled
	// 如果 context 被取消，则返回一个 error
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Determine wait limit
	now := time.Now()
	waitLimit := InfDuration
	if deadline, ok := ctx.Deadline(); ok {
		waitLimit = deadline.Sub(now)
	}
	// Reserve
	r := lim.reserveN(now, n, waitLimit)
	if !r.ok {
		return fmt.Errorf("rate: Wait(n=%d) would exceed context deadline", n)
	}

	// Wait if necessary
	// 从现在开始，到令牌准备好，一共需要多长时间
	delay := r.DelayFrom(now)
	// 如果是 0 则表示可以已经准备就绪，可以返回了
	if delay == 0 {
		return nil
	}

	// 开启一个计数器，时间为 delay
	t := time.NewTimer(delay)
	defer t.Stop()

	// WaitN 会被阻塞就是因为这里使用了 select，直到计时器
	// 到时，或者收到 context 的取消信号才会停止阻塞
	select {
	case <-t.C:
		// We can proceed.
		return nil
	case <-ctx.Done():
		// Context was canceled before we could proceed.  Cancel the
		// reservation, which may permit other events to proceed sooner.
		r.Cancel()
		return ctx.Err()
	}
}

// SetLimit is shorthand for SetLimitAt(time.Now(), newLimit).
func (lim *Limiter) SetLimit(newLimit Limit) {
	lim.SetLimitAt(time.Now(), newLimit)
}

// SetLimitAt sets a new Limit for the limiter. The new Limit, and Burst, may be violated
// or underutilized by those which reserved (using Reserve or Wait) but did not yet act
// before SetLimitAt was called.
func (lim *Limiter) SetLimitAt(now time.Time, newLimit Limit) {
	lim.mu.Lock()
	defer lim.mu.Unlock()

	now, _, tokens := lim.advance(now)

	lim.last = now
	lim.tokens = tokens
	lim.limit = newLimit
}

// SetBurst is shorthand for SetBurstAt(time.Now(), newBurst).
func (lim *Limiter) SetBurst(newBurst int) {
	lim.SetBurstAt(time.Now(), newBurst)
}

// SetBurstAt sets a new burst size for the limiter.
func (lim *Limiter) SetBurstAt(now time.Time, newBurst int) {
	lim.mu.Lock()
	defer lim.mu.Unlock()

	now, _, tokens := lim.advance(now)

	lim.last = now
	lim.tokens = tokens
	lim.burst = newBurst
}

// reserveN is a helper method for AllowN, ReserveN, and WaitN.
// maxFutureReserve specifies the maximum reservation wait duration allowed.
// reserveN returns Reservation, not *Reservation, to avoid allocation in AllowN and WaitN.
//
// reserveN 是 AllowN、ReserveN 和 WaitN 的辅助方法。
// maxFutureReserve 指定允许的最大等待时间。
// reserveN 返回 Reservation，而不是 *Reservation，以避免在 AllowN 和 WaitN 中分配。
func (lim *Limiter) reserveN(now time.Time, n int, maxFutureReserve time.Duration) Reservation {
	lim.mu.Lock()

	// 如果没有限流则直接返回
	if lim.limit == Inf {
		lim.mu.Unlock()
		return Reservation{
			ok:        true, // 桶中有足够的令牌
			lim:       lim,
			tokens:    n,
			timeToAct: now,
		}
	}

	// 更新令牌桶的状态，tokens 为目前可用的令牌数量
	now, last, tokens := lim.advance(now)

	// Calculate the remaining number of tokens resulting from the request.
	// 可用的令牌数 tokens 减去需要获取的令牌数(n)
	tokens -= float64(n)

	// Calculate the wait duration
	// 如果 tokens 小于 0 ，则说明桶中没有足够的令牌，计算出产生这些缺数的令牌需要多久
	var waitDuration time.Duration
	if tokens < 0 {
		// 计算出所需时间
		waitDuration = lim.limit.durationFromTokens(-tokens)
	}

	// Decide result
	// 如果 n 小于等于令牌桶的容量，并且允许的最大等待时间（maxFutureReserve）小于等于
	// 产生所差令牌需要的时间，则 ok 为 true
	ok := n <= lim.burst && waitDuration <= maxFutureReserve

	// Prepare reservation
	r := Reservation{
		ok:    ok,
		lim:   lim,
		limit: lim.limit,
	}
	if ok {
		r.tokens = n
		// timeToAct 需要等待的时间点，到此时才能产生预约所需数量的令牌
		r.timeToAct = now.Add(waitDuration)
	}

	// Update state
	if ok {
		lim.last = now              // 上次 tokens 更新的时间赋值为 now
		lim.tokens = tokens         // 更新可用令牌数
		lim.lastEvent = r.timeToAct //
	} else {
		lim.last = last
	}

	lim.mu.Unlock()
	return r
}

// advance calculates and returns an updated state for lim resulting from the passage of time.
// lim is not changed.
// advance requires that lim.mu is held.
//
// advance 计算自上次更新（lim.last）到 now（参数）这段时间里，Limiter 的状态发生了怎样的改变
// newNow: 貌似是原封不动地把参数 now 返回出去
// newLast:
// newTokens: 更新后的令牌数量
func (lim *Limiter) advance(now time.Time) (newNow time.Time, newLast time.Time, newTokens float64) {
	// last 不能在当前时间 now 之后，否则计算出来的 elapsed 为负数，会导致令牌桶数量减少
	last := lim.last
	if now.Before(last) {
		last = now
	}

	// Calculate the new number of tokens, due to time that passed.
	// elapsed 表示从 last 到 now 已经过了多长时间
	elapsed := now.Sub(last)
	// 计算 elapsed 时间内可以生成多少个令牌
	delta := lim.limit.tokensFromDuration(elapsed)
	// tokens = elapsed 时间内生成的令牌数 + 原先已有的令牌数
	tokens := lim.tokens + delta
	// tokens 不能超过桶的容量
	if burst := float64(lim.burst); tokens > burst {
		tokens = burst
	}
	return now, last, tokens
}

// durationFromTokens is a unit conversion function from the number of tokens to the duration
// of time it takes to accumulate them at a rate of limit tokens per second.
//
// durationFromTokens 计算生成 tokens 个 token 需要多长时间
func (limit Limit) durationFromTokens(tokens float64) time.Duration {
	seconds := tokens / float64(limit)
	return time.Duration(float64(time.Second) * seconds)
}

// tokensFromDuration is a unit conversion function from a time duration to the number of tokens
// which could be accumulated during that duration at a rate of limit tokens per second.
//
// tokensFromDuration 是一个转换函数，用来计算在时间 d 内一共可以生成
// 多少个 token（以 limit 为单位）
func (limit Limit) tokensFromDuration(d time.Duration) float64 {
	return d.Seconds() * float64(limit)
}
