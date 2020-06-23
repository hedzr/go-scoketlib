package rb

import (
	"fmt"
	"go.uber.org/zap"
	"io"
	"sync/atomic"
	"time"
	"unsafe"
)

type (
	Queue interface {
		Enqueue(item interface{}) (err error)
		Dequeue() (item interface{}, err error)
		// Cap returns the outer capacity of the ring buffer.
		Cap() uint32
		// Size returns the quantity of items in the ring buffer queue
		Size() uint32
		IsEmpty() (b bool)
		IsFull() (b bool)
	}

	RingBuffer interface {
		io.Closer // for logger

		Queue

		Put(item interface{}) (err error)
		Get() (item interface{}, err error)

		// Quantity returns the quantity of items in the ring buffer queue
		Quantity() uint32

		// // Cap returns the outer capacity of the ring buffer.
		// Cap() uint32
		// IsEmpty() (b bool)
		// IsFull() (b bool)

		Debug(enabled bool) (lastState bool)

		ResetCounters()
	}

	ringBuf struct {
		// isEmpty bool
		cap        uint32
		capModMask uint32
		head       uint32
		tail       uint32
		putWaits   uint64
		getWaits   uint64
		_          [CacheLinePadSize - 8 - 8 - 4*4]byte
		data       []rbItem
		debugMode  bool
		logger     *zap.Logger
		// _         cpu.CacheLinePad
	}

	rbItem struct {
		readWrite uint64      // 0: writable, 1: readable, 2: write ok, 3: read ok
		value     interface{} // ptr
		_         [CacheLinePadSize - 8 - 8]byte
		// _         cpu.CacheLinePad
	}

	ringer struct {
		cap uint32
		// _         [CacheLinePadSize-unsafe.Sizeof(uint32)]byte
	}
)

func (rb *ringBuf) Put(item interface{}) (err error) {
	err = rb.Enqueue(item)
	return
}

func (rb *ringBuf) Enqueue(item interface{}) (err error) {
	var tail, head, nt uint32
	var holder *rbItem
	for {
		var quad uint64
		quad = atomic.LoadUint64((*uint64)(unsafe.Pointer(&rb.head)))
		head = (uint32)(quad & MaxUint32_64)
		tail = (uint32)(quad >> 32)
		// head = atomic.LoadUint32(&rb.head)
		// tail = atomic.LoadUint32(&rb.tail)
		nt = (tail + 1) & rb.capModMask

		isFull := nt == head
		if isFull {
			err = ErrQueueFull
			return
		}

		holder = &rb.data[tail]

		if atomic.CompareAndSwapUint64(&holder.readWrite, 0, 2) {
			holder.value = item
			atomic.CompareAndSwapUint32(&rb.tail, tail, nt)
			break
		}

		time.Sleep(1 * time.Nanosecond)
		atomic.AddUint64(&rb.putWaits, 1)
	}

	if !atomic.CompareAndSwapUint64(&holder.readWrite, 2, 1) {
		err = fmt.Errorf("[W] %w, 2=>1, %v", ErrRaced, holder.readWrite)
		return
	}

	// if rb.debugMode {
	// 	rb.logger.Debug("[ringbuf][PUT] ", zap.Uint32("cap", rb.cap), zap.Uint32("qty", rb.qty(head, tail)), zap.Uint32("tail", tail), zap.Uint32("new tail", nt), zap.Uint32("head", head))
	// }
	return
}

func (rb *ringBuf) Get() (item interface{}, err error) {
	item, err = rb.Dequeue()
	return
}

func (rb *ringBuf) Dequeue() (item interface{}, err error) {
	var tail, head, nh uint32
	var holder *rbItem
	for {
		var quad uint64
		quad = atomic.LoadUint64((*uint64)(unsafe.Pointer(&rb.head)))
		head = (uint32)(quad & MaxUint32_64)
		tail = (uint32)(quad >> 32)
		// head = atomic.LoadUint32(&rb.head)
		// tail = atomic.LoadUint32(&rb.tail)

		isEmpty := head == tail
		if isEmpty {
			err = ErrQueueEmpty
			return
		}

		holder = &rb.data[head]

		if atomic.CompareAndSwapUint64(&holder.readWrite, 1, 3) {
			// cnt := 0
			// for {
			// 	item = holder.value
			// 	nh = (head + 1) & rb.capModMask
			// 	if atomic.CompareAndSwapUint32(&rb.head, head, nh) {
			// 		break
			// 	}
			//
			// 	// err = fmt.Errorf("[R] %w, head: %v=>%v", ErrRaced, head, nh)
			// 	// return
			//
			// 	time.Sleep(1 * time.Nanosecond)
			// 	atomic.AddUint64(&rb.getWaits, 1)
			//
			// 	cnt++
			// 	if cnt > 100 {
			// 		atomic.CompareAndSwapUint64(&holder.readWrite, 3, 1)
			// 		err = fmt.Errorf("[R] %w, head: %v => %v, tail: %v, 100 retried/getWaits=%v", ErrRaced, head, nh, tail, rb.getWaits)
			// 		return
			// 	}
			// }

			item = holder.value
			nh = (head + 1) & rb.capModMask
			atomic.CompareAndSwapUint32(&rb.head, head, nh)
			break
		}

		time.Sleep(1 * time.Nanosecond)
		atomic.AddUint64(&rb.getWaits, 1)
	}

	// if rb.debugMode {
	// 	rb.logger.Debug("[ringbuf][GET] ", zap.Uint32("cap", rb.cap), zap.Uint32("qty", rb.qty(head, tail)), zap.Uint32("tail", tail), zap.Uint32("head", head), zap.Uint32("new head", nh))
	// }

	if !atomic.CompareAndSwapUint64(&holder.readWrite, 3, 0) {
		err = fmt.Errorf("[R] %w, 3=>0, %v", ErrRaced, holder.readWrite)
		return
	}

	if item == nil {
		err = fmt.Errorf("[ringbuf][GET] cap: %v, qty: %v, head: %v, tail: %v, new head: %v", rb.cap, rb.qty(head, tail), head, tail, nh)

		if !rb.debugMode {
			rb.logger.Warn("[ringbuf][GET] ", zap.Uint32("cap", rb.cap), zap.Uint32("qty", rb.qty(head, tail)), zap.Uint32("tail", tail), zap.Uint32("head", head), zap.Uint32("new head", nh))
		}
		rb.logger.Fatal("[ringbuf][GET] [ERR] unexpected nil element value FOUND!")
	}
	return
}
