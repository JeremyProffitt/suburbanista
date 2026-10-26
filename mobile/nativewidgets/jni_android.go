//go:build android && widgetbind

package nativewidgets

import "git.wow.st/gmp/jni"

// All references belong to one attached thread and are released before return.
type native struct {
	env  jni.Env
	refs []jni.Object
}

func newNative(env jni.Env) *native { return &native{env: env} }
func (n *native) close() {
	for i := len(n.refs) - 1; i >= 0; i-- {
		jni.DeleteLocalRef(n.env, n.refs[i])
	}
}
func (n *native) keep(o jni.Object) jni.Object {
	if o != 0 {
		n.refs = append(n.refs, o)
	}
	return o
}
func (n *native) class(name string) jni.Class {
	return jni.Class(n.keep(jni.Object(jni.FindClass(n.env, name))))
}
func (n *native) str(s string) jni.Value {
	return jni.Value(n.keep(jni.Object(jni.JavaString(n.env, s))))
}
func (n *native) must(o jni.Object, e error) jni.Object {
	if e != nil {
		panic("Android widget call failed")
	}
	return n.keep(o)
}
func (n *native) call(o jni.Object, name, sig string, args ...jni.Value) jni.Object {
	c := jni.GetObjectClass(n.env, o)
	n.keep(jni.Object(c))
	return n.must(jni.CallObjectMethod(n.env, o, jni.GetMethodID(n.env, c, name, sig), args...))
}
func (n *native) void(o jni.Object, name, sig string, args ...jni.Value) {
	c := jni.GetObjectClass(n.env, o)
	n.keep(jni.Object(c))
	if jni.CallVoidMethod(n.env, o, jni.GetMethodID(n.env, c, name, sig), args...) != nil {
		panic("Android widget call failed")
	}
}
func (n *native) static(class, name, sig string, args ...jni.Value) jni.Object {
	c := n.class(class)
	return n.must(jni.CallStaticObjectMethod(n.env, c, jni.GetStaticMethodID(n.env, c, name, sig), args...))
}
func (n *native) integer(v int) jni.Object {
	return n.static("java/lang/Integer", "valueOf", "(I)Ljava/lang/Integer;", jni.Value(v))
}
func (n *native) intCall(o jni.Object, name, sig string, args ...jni.Value) int {
	c := jni.GetObjectClass(n.env, o)
	n.keep(jni.Object(c))
	v, e := jni.CallIntMethod(n.env, o, jni.GetMethodID(n.env, c, name, sig), args...)
	if e != nil {
		panic("Android widget integer unavailable")
	}
	return int(v)
}
func (n *native) staticInt(class, name, sig string, args ...jni.Value) int {
	c := n.class(class)
	v, e := jni.CallStaticIntMethod(n.env, c, jni.GetStaticMethodID(n.env, c, name, sig), args...)
	if e != nil {
		panic("Android widget integer unavailable")
	}
	return int(v)
}
func (n *native) intType() jni.Object {
	c := n.class("java/lang/Integer")
	return n.keep(jni.GetStaticObjectField(n.env, c, jni.GetStaticFieldID(n.env, c, "TYPE", "Ljava/lang/Class;")))
}
func (n *native) new(class string, types, args []jni.Object) jni.Object {
	c := n.class(class)
	cc := n.class("java/lang/Class")
	ts := jni.NewObjectArray(n.env, jni.Size(len(types)), cc, 0)
	n.keep(jni.Object(ts))
	for i, t := range types {
		if jni.SetObjectArrayElement(n.env, ts, jni.Size(i), t) != nil {
			panic("constructor types")
		}
	}
	ctor := n.must(jni.CallObjectMethod(n.env, jni.Object(c), jni.GetMethodID(n.env, cc, "getConstructor", "([Ljava/lang/Class;)Ljava/lang/reflect/Constructor;"), jni.Value(ts)))
	as := jni.NewObjectArray(n.env, jni.Size(len(args)), n.class("java/lang/Object"), 0)
	n.keep(jni.Object(as))
	for i, a := range args {
		if jni.SetObjectArrayElement(n.env, as, jni.Size(i), a) != nil {
			panic("constructor values")
		}
	}
	return n.call(ctor, "newInstance", "([Ljava/lang/Object;)Ljava/lang/Object;", jni.Value(as))
}
