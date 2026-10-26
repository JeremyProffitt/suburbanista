//go:build android

package keystore

import (
	"errors"
	"git.wow.st/gmp/jni"
)

// Crypt preserves the existing Keystore alias, AAD and version-one envelope.
func Crypt(env jni.Env, input []byte, encrypt bool) (output []byte, err error) {
	defer func() {
		if recover() != nil {
			output = nil
			err = errors.New("Keystore operation failed")
		}
	}()
	refs := []jni.Object{}
	defer func() {
		for i := len(refs) - 1; i >= 0; i-- {
			jni.DeleteLocalRef(env, refs[i])
		}
	}()
	keep := func(o jni.Object) jni.Object {
		if o != 0 {
			refs = append(refs, o)
		}
		return o
	}
	cls := func(s string) jni.Class { c := jni.FindClass(env, s); keep(jni.Object(c)); return c }
	str := func(s string) jni.Value { return jni.Value(keep(jni.Object(jni.JavaString(env, s)))) }
	must := func(o jni.Object, e error) jni.Object {
		if e != nil {
			panic("jni")
		}
		return keep(o)
	}
	check := func(e error) {
		if e != nil {
			panic("jni")
		}
	}
	// The pinned JNI dependency's NewObject wrapper passes a jvalue
	// array to JNI's variadic NewObject, which aborts ART. Use Java's
	// public reflection constructor API through the correct object-call
	// wrappers; no application native glue or dependency patch is needed.
	construct := func(class jni.Class, types []jni.Object, args []jni.Object) jni.Object {
		classClass := cls("java/lang/Class")
		parameterTypes := jni.NewObjectArray(env, jni.Size(len(types)), classClass, 0)
		keep(jni.Object(parameterTypes))
		for i, typ := range types {
			check(jni.SetObjectArrayElement(env, parameterTypes, jni.Size(i), typ))
		}
		constructor := must(jni.CallObjectMethod(env, jni.Object(class), jni.GetMethodID(env, classClass, "getConstructor", "([Ljava/lang/Class;)Ljava/lang/reflect/Constructor;"), jni.Value(parameterTypes)))
		arguments := jni.NewObjectArray(env, jni.Size(len(args)), cls("java/lang/Object"), 0)
		keep(jni.Object(arguments))
		for i, arg := range args {
			check(jni.SetObjectArrayElement(env, arguments, jni.Size(i), arg))
		}
		constructorClass := cls("java/lang/reflect/Constructor")
		return must(jni.CallObjectMethod(env, constructor, jni.GetMethodID(env, constructorClass, "newInstance", "([Ljava/lang/Object;)Ljava/lang/Object;"), jni.Value(arguments)))
	}
	integerClass := cls("java/lang/Integer")
	integerType := keep(jni.GetStaticObjectField(env, integerClass, jni.GetStaticFieldID(env, integerClass, "TYPE", "Ljava/lang/Class;")))
	boxedInt := func(value int) jni.Object {
		return must(jni.CallStaticObjectMethod(env, integerClass, jni.GetStaticMethodID(env, integerClass, "valueOf", "(I)Ljava/lang/Integer;"), jni.Value(value)))
	}
	ksClass := cls("java/security/KeyStore")
	ks := must(jni.CallStaticObjectMethod(env, ksClass, jni.GetStaticMethodID(env, ksClass, "getInstance", "(Ljava/lang/String;)Ljava/security/KeyStore;"), str("AndroidKeyStore")))
	check(jni.CallVoidMethod(env, ks, jni.GetMethodID(env, ksClass, "load", "(Ljava/security/KeyStore$LoadStoreParameter;)V"), 0))
	alias := str("suburban-remote-session-v1")
	exists, e := jni.CallBooleanMethod(env, ks, jni.GetMethodID(env, ksClass, "containsAlias", "(Ljava/lang/String;)Z"), alias)
	check(e)
	if !exists {
		if !encrypt {
			panic("missing key")
		}
		builderClass := cls("android/security/keystore/KeyGenParameterSpec$Builder")
		builder := construct(builderClass, []jni.Object{jni.Object(cls("java/lang/String")), integerType}, []jni.Object{jni.Object(alias), boxedInt(3)})
		strings := cls("java/lang/String")
		array := func(s string) jni.Value {
			a := jni.NewObjectArray(env, 1, strings, jni.Object(str(s)))
			keep(jni.Object(a))
			return jni.Value(a)
		}
		must(jni.CallObjectMethod(env, builder, jni.GetMethodID(env, builderClass, "setBlockModes", "([Ljava/lang/String;)Landroid/security/keystore/KeyGenParameterSpec$Builder;"), array("GCM")))
		must(jni.CallObjectMethod(env, builder, jni.GetMethodID(env, builderClass, "setEncryptionPaddings", "([Ljava/lang/String;)Landroid/security/keystore/KeyGenParameterSpec$Builder;"), array("NoPadding")))
		must(jni.CallObjectMethod(env, builder, jni.GetMethodID(env, builderClass, "setKeySize", "(I)Landroid/security/keystore/KeyGenParameterSpec$Builder;"), 256))
		spec := must(jni.CallObjectMethod(env, builder, jni.GetMethodID(env, builderClass, "build", "()Landroid/security/keystore/KeyGenParameterSpec;")))
		genClass := cls("javax/crypto/KeyGenerator")
		gen := must(jni.CallStaticObjectMethod(env, genClass, jni.GetStaticMethodID(env, genClass, "getInstance", "(Ljava/lang/String;Ljava/lang/String;)Ljavax/crypto/KeyGenerator;"), str("AES"), str("AndroidKeyStore")))
		check(jni.CallVoidMethod(env, gen, jni.GetMethodID(env, genClass, "init", "(Ljava/security/spec/AlgorithmParameterSpec;)V"), jni.Value(spec)))
		must(jni.CallObjectMethod(env, gen, jni.GetMethodID(env, genClass, "generateKey", "()Ljavax/crypto/SecretKey;")))
	}
	key := must(jni.CallObjectMethod(env, ks, jni.GetMethodID(env, ksClass, "getKey", "(Ljava/lang/String;[C)Ljava/security/Key;"), alias, 0))
	cipherClass := cls("javax/crypto/Cipher")
	cipher := must(jni.CallStaticObjectMethod(env, cipherClass, jni.GetStaticMethodID(env, cipherClass, "getInstance", "(Ljava/lang/String;)Ljavax/crypto/Cipher;"), str("AES/GCM/NoPadding")))
	var iv []byte
	if encrypt {
		check(jni.CallVoidMethod(env, cipher, jni.GetMethodID(env, cipherClass, "init", "(ILjava/security/Key;)V"), 1, jni.Value(key)))
	} else {
		if len(input) < 29 || input[0] != 1 {
			panic("invalid envelope")
		}
		iv = input[1:13]
		input = input[13:]
		ivArray := jni.NewByteArray(env, iv)
		keep(jni.Object(ivArray))
		gcmClass := cls("javax/crypto/spec/GCMParameterSpec")
		spec := construct(gcmClass, []jni.Object{integerType, jni.Object(cls("[B"))}, []jni.Object{boxedInt(128), jni.Object(ivArray)})
		check(jni.CallVoidMethod(env, cipher, jni.GetMethodID(env, cipherClass, "init", "(ILjava/security/Key;Ljava/security/spec/AlgorithmParameterSpec;)V"), 2, jni.Value(key), jni.Value(spec)))
	}
	aad := jni.NewByteArray(env, []byte("ninja.jeremy.suburbanremote/session/v1"))
	keep(jni.Object(aad))
	check(jni.CallVoidMethod(env, cipher, jni.GetMethodID(env, cipherClass, "updateAAD", "([B)V"), jni.Value(aad)))
	data := jni.NewByteArray(env, input)
	keep(jni.Object(data))
	result := must(jni.CallObjectMethod(env, cipher, jni.GetMethodID(env, cipherClass, "doFinal", "([B)[B"), jni.Value(data)))
	output = jni.GetByteArrayElements(env, jni.ByteArray(result))
	if encrypt {
		ivObj := must(jni.CallObjectMethod(env, cipher, jni.GetMethodID(env, cipherClass, "getIV", "()[B")))
		iv = jni.GetByteArrayElements(env, jni.ByteArray(ivObj))
		if len(iv) != 12 {
			panic("invalid iv")
		}
		output = append(append([]byte{1}, iv...), output...)
	}

	return output, nil
}
